package bundle_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/bundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/expansion"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/outbound"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
	"gopkg.in/yaml.v3"
)

// The fixture project root is long enough that the fixed V1 alias is strictly
// shorter than the real root, which is the condition for an active mapping
// (requirements.md 1.4). The expected alias is spelled out literally rather than
// recomputed, so a drift in the tag algorithm cannot make these tests agree with a
// wrong implementation.
const (
	fixtureRoot    = "/home/dev/projects/go-llm-interactive-proxy"
	fixtureAlias   = "/.__lip_v1__/w_ylfucd77chy74zh3qwma/"
	fixtureSuffix  = "pkg/lipapi/call.go"
	fixtureTarget  = fixtureRoot + "/" + fixtureSuffix
	fixtureVirtual = fixtureAlias + fixtureSuffix
	fixtureTool    = "read_file"
	fixtureCallID  = "call_7f3a"
	fixtureArgs    = `{"file_path":"` + fixtureTarget + `"}`
)

// pinnedWorkspace is the runtime's per-turn PIN, projected onto the public SDK
// context seam both outbound stages read. It is built once per request in production
// and handed to the attempt stage as metadata and to the request-part stage as this
// projection, which is what lets one bundle arm both passes from one authority.
func pinnedWorkspace(root string) context.Context {
	return lipworkspace.WithWorkspaceView(context.Background(),
		lipworkspace.WorkspaceView{ID: "ws_fixture", ProjectRoot: root})
}

func node(t *testing.T, src string) yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return doc
}

func resolve(t *testing.T, src string) config.Resolved {
	t.Helper()
	resolved, err := config.Decode(node(t, src))
	if err != nil {
		t.Fatalf("Decode(%q): %v", src, err)
	}
	return resolved
}

// pathCall is one historical item-authoritative path-bearing tool call. The tool
// declaration matters: it is what lets the shipped built-in profile layer claim
// /file_path without any operator configuration.
func pathCall() *lipapi.Call {
	return &lipapi.Call{
		ID:    "call_bundle",
		Route: lipapi.RouteIntent{Selector: "bundle:m"},
		Tools: []lipapi.ToolDef{{
			Name:       fixtureTool,
			Parameters: []byte(`{"type":"object","properties":{"file_path":{"type":"string"}}}`),
		}},
		Items: []lipapi.Item{{
			Kind: lipapi.ItemKindToolCall, ID: "item_call", Status: lipapi.ItemStatusCompleted,
			ToolCall: &lipapi.ToolCallItem{
				CallID:    fixtureCallID,
				Name:      fixtureTool,
				Arguments: json.RawMessage(fixtureArgs),
			},
		}},
	}
}

func arguments(t *testing.T, call *lipapi.Call) string {
	t.Helper()
	if len(call.Items) != 1 || call.Items[0].ToolCall == nil {
		t.Fatalf("expected one item tool call, got %#v", call.Items)
	}
	return string(call.Items[0].ToolCall.Arguments)
}

func armedBundle(t *testing.T, src string) lipfeature.FeatureBundle {
	t.Helper()
	b, err := bundle.FeatureBundle(resolve(t, src))
	if err != nil {
		t.Fatalf("FeatureBundle: %v", err)
	}
	return b
}

func TestTheBundleContributesExactlyThreeComponentsThroughExistingPlanes(t *testing.T) {
	t.Parallel()
	b := armedBundle(t, "enabled: true\nmode: rewrite\n")
	if err := b.Validate(); err != nil {
		t.Fatalf("bundle.Validate: %v", err)
	}

	transforms := lipfeature.Get(b.PlaneSet, lipfeature.PlaneAttemptTransforms)
	if len(transforms) != 1 {
		t.Fatalf("attempt transforms = %d, want 1", len(transforms))
	}
	if transforms[0].ID() != outbound.TransformID {
		t.Fatalf("attempt transform id = %q, want %q", transforms[0].ID(), outbound.TransformID)
	}
	if transforms[0].Order() != outbound.OrderAttemptTransform {
		t.Fatalf("attempt transform order = %d, want %d", transforms[0].Order(), outbound.OrderAttemptTransform)
	}

	hooks := lipfeature.Get(b.PlaneSet, lipfeature.PlaneRequestPartHooks)
	if len(hooks) != 1 {
		t.Fatalf("request part hooks = %d, want 1", len(hooks))
	}
	if hooks[0].ID() != outbound.PartHookID {
		t.Fatalf("request part hook id = %q, want %q", hooks[0].ID(), outbound.PartHookID)
	}
	if hooks[0].Order() != outbound.OrderRequestPartHook {
		t.Fatalf("request part hook order = %d, want %d", hooks[0].Order(), outbound.OrderRequestPartHook)
	}

	finalizers := lipfeature.Get(b.PlaneSet, lipfeature.PlaneToolCallFinalizers)
	if len(finalizers) != 1 {
		t.Fatalf("tool call finalizers = %d, want 1", len(finalizers))
	}
	if finalizers[0].ID() != expansion.FinalizerID {
		t.Fatalf("finalizer id = %q, want %q", finalizers[0].ID(), expansion.FinalizerID)
	}
	if finalizers[0].Order() != expansion.FinalizerOrder {
		t.Fatalf("finalizer order = %d, want %d", finalizers[0].Order(), expansion.FinalizerOrder)
	}
}

// TestTheBundleContributesNoNewPlane is the structural half of design.md
// "Configuration and Composition": the three components travel through already
// registered planes only, and the two planes a naive composition might have
// invented stay empty.
func TestTheBundleContributesNoNewPlane(t *testing.T) {
	t.Parallel()
	b := armedBundle(t, "enabled: true\nmode: rewrite\n")
	if len(b.Lifecycles) != 0 {
		t.Fatalf("lifecycles = %d, want 0", len(b.Lifecycles))
	}
	// No workspace authority of its own: the feature consumes the composition
	// root's instance rather than publishing one into the authoritative chain.
	if got := lipfeature.Get(b.PlaneSet, lipfeature.PlaneWorkspaceResolvers); len(got) != 0 {
		t.Fatalf("workspace resolvers = %d, want 0: the feature publishes no authority", len(got))
	}
	// No restated completeness bound: the finalizer carries the declaration itself
	// as the SDK's BufferingRequirement capability (Task 7.3), so a separate
	// contribution here would be a duplicate fact that could drift.
	if got := lipfeature.Get(b.PlaneSet, lipfeature.PlaneToolCallFinalizationMaxArgsBytes); got != 0 {
		t.Fatalf("finalization max args bytes = %d, want 0", got)
	}
}

func TestADisabledResolutionPublishesNoPlaneAtAll(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{name: "absent", yaml: ""},
		{name: "null", yaml: "null\n"},
		{name: "empty mapping", yaml: "{}\n"},
		{name: "explicitly disabled", yaml: "enabled: false\nmode: rewrite\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resolved := resolve(t, tc.yaml)
			if !resolved.Disabled() {
				t.Fatal("expected an inert resolution")
			}
			b, err := bundle.FeatureBundle(resolved)
			if err != nil {
				t.Fatalf("FeatureBundle: %v", err)
			}
			if !b.PlaneSet.IsZero() {
				t.Fatalf("disabled resolution published planes: %#v", b.PlaneSet)
			}
			if len(lipfeature.Get(b.PlaneSet, lipfeature.PlaneAttemptTransforms)) != 0 ||
				len(lipfeature.Get(b.PlaneSet, lipfeature.PlaneRequestPartHooks)) != 0 ||
				len(lipfeature.Get(b.PlaneSet, lipfeature.PlaneToolCallFinalizers)) != 0 {
				t.Fatal("disabled resolution contributed a component")
			}
		})
	}
}

// TestARewriteModeRegistrationActuallyMutates is the mode obligation: the mode
// crosses the bundle boundary as a value, so a rewrite registration cannot degrade
// into the engine's zero value (audit) and silently measure a rollout the operator
// asked to mutate.
func TestARewriteModeRegistrationActuallyMutates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		yaml     string
		wantMode rewrite.Mode
	}{
		{name: "rewrite", yaml: "enabled: true\nmode: rewrite\n", wantMode: rewrite.ModeRewrite},
		{name: "audit", yaml: "enabled: true\nmode: audit\n", wantMode: rewrite.ModeAudit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resolved := resolve(t, tc.yaml)
			if resolved.Mode() != tc.wantMode {
				t.Fatalf("resolved mode = %v, want %v", resolved.Mode(), tc.wantMode)
			}
			b := armedBundle(t, tc.yaml)
			transform := lipfeature.Get(b.PlaneSet, lipfeature.PlaneAttemptTransforms)[0]

			before := pathCall()
			after := pathCall()
			decision, err := transform.HandleAttempt(context.Background(), after,
				request.AttemptMeta{Workspace: lipworkspace.WorkspaceView{ProjectRoot: fixtureRoot}},
				request.Services{})
			if err != nil {
				t.Fatalf("HandleAttempt: %v", err)
			}
			if decision.Kind != request.AttemptContinue {
				t.Fatalf("decision = %#v, want AttemptContinue", decision.Kind)
			}
			got := arguments(t, after)
			switch tc.wantMode {
			case rewrite.ModeRewrite:
				if got == arguments(t, before) {
					t.Fatalf("rewrite mode published nothing: %s", got)
				}
				if strings.Contains(got, fixtureRoot) {
					t.Fatalf("rewrite mode left the real root in the published arguments: %s", got)
				}
				if !strings.Contains(got, fixtureVirtual) {
					t.Fatalf("rewrite mode published %q, want the pinned virtual alias %q", got, fixtureVirtual)
				}
			case rewrite.ModeAudit:
				if got != arguments(t, before) {
					t.Fatalf("audit mode mutated canonical arguments: %s", got)
				}
			}
		})
	}
}

// TestTheBundleReadsTheRuntimesPinnedWorkspaceView is the authority rule in the shape
// the shipped composition actually has: the bundle takes no workspace authority at all,
// and the late pass is armed purely by the PIN the runtime projected onto the public SDK
// context seam. The rewrite it publishes is the pinned alias, which is the same value
// the early pass derives from the same pin.
func TestTheBundleReadsTheRuntimesPinnedWorkspaceView(t *testing.T) {
	t.Parallel()
	b := armedBundle(t, "enabled: true\nmode: rewrite\n")
	hook := lipfeature.Get(b.PlaneSet, lipfeature.PlaneRequestPartHooks)[0]

	call := pathCall()
	if err := hook.HandleRequestParts(pinnedWorkspace(fixtureRoot), call, sdkhooks.PartMeta{}); err != nil {
		t.Fatalf("HandleRequestParts: %v", err)
	}
	got := arguments(t, call)
	if !strings.Contains(got, fixtureVirtual) {
		t.Fatalf("the late pass published %q, want the pinned virtual alias %q", got, fixtureVirtual)
	}
	if strings.Contains(got, fixtureRoot) {
		t.Fatalf("requirements.md 5.2 - the late pass left a real root at the backend bound: %s", got)
	}
}

// TestTheBundleIsInertWithoutAPinnedWorkspaceView is the documented fail-open
// condition, and it is now the ABSENCE of the projection rather than an unbound
// resolver. requirements.md 8.1 makes an unconfigured feature unobservable and 5.7
// forbids substituting any other root source, so the pass publishes nothing and keeps
// the real path.
//
// The DETACHED auxiliary path is exactly this state: the executor pins an empty
// workspace view there (internal/core/runtime/executor_prepare_detached.go), which
// projects an attached view with no project root, so DeriveMapping - not the authority
// lookup - refuses it. That case is asserted separately below.
func TestTheBundleIsInertWithoutAPinnedWorkspaceView(t *testing.T) {
	t.Parallel()
	b := armedBundle(t, "enabled: true\nmode: rewrite\n")
	hook := lipfeature.Get(b.PlaneSet, lipfeature.PlaneRequestPartHooks)[0]

	call := pathCall()
	if err := hook.HandleRequestParts(context.Background(), call, sdkhooks.PartMeta{}); err != nil {
		t.Fatalf("an absent pinned view must fail open, not error: %v", err)
	}
	if got := arguments(t, call); got != fixtureArgs {
		t.Fatalf("the late pass published a rewrite it could not justify: %s", got)
	}
}

// TestTheLatePassRevirtualizesARealPathLateShapingRestored is requirements.md 5.4 at
// the composition boundary: a real path put back onto path-bearing tool history AFTER
// the early pass ran is re-virtualized, so the backend-bound request cannot carry it.
//
// This is the enforcement the previous composition could not offer. With the authority
// bound to an unbound default, the shipped bundle's late pass was inert, and a restored
// real path reached the backend untouched - the violation shape
// internal/core/runtime/path_virtualization_request_part_hook_regression_test.go names
// "a real path at Backend.Open".
func TestTheLatePassRevirtualizesARealPathLateShapingRestored(t *testing.T) {
	t.Parallel()
	b := armedBundle(t, "enabled: true\nmode: rewrite\n")
	transform := lipfeature.Get(b.PlaneSet, lipfeature.PlaneAttemptTransforms)[0]
	hook := lipfeature.Get(b.PlaneSet, lipfeature.PlaneRequestPartHooks)[0]

	// The early pass virtualizes the one historical path-bearing call.
	call := pathCall()
	if _, err := transform.HandleAttempt(pinnedWorkspace(fixtureRoot), call,
		request.AttemptMeta{Workspace: lipworkspace.WorkspaceView{ProjectRoot: fixtureRoot}},
		request.Services{}); err != nil {
		t.Fatalf("HandleAttempt: %v", err)
	}
	first := arguments(t, call)
	if !strings.Contains(first, fixtureVirtual) {
		t.Fatalf("fixture: the early pass must have virtualized the historical call: %s", first)
	}

	// Later request shaping reintroduces a real-root call, which is the hazard the late
	// pass exists to catch.
	call.Items = append(call.Items, lipapi.Item{
		Kind: lipapi.ItemKindToolCall, ID: "item_call_late", Status: lipapi.ItemStatusCompleted,
		ToolCall: &lipapi.ToolCallItem{
			CallID: "call_late", Name: fixtureTool,
			Arguments: json.RawMessage(`{"file_path":"` + fixtureRoot +
				`/internal/core/runtime/executor.go","limit":10}`),
		},
	})

	if err := hook.HandleRequestParts(pinnedWorkspace(fixtureRoot), call, sdkhooks.PartMeta{}); err != nil {
		t.Fatalf("HandleRequestParts: %v", err)
	}
	if got := string(call.Items[0].ToolCall.Arguments); got != first {
		t.Fatalf("requirements.md 2.9 - the late pass must not disturb the surface the early pass already virtualized: %s", got)
	}
	lateArgs := string(call.Items[1].ToolCall.Arguments)
	if !strings.Contains(lateArgs, fixtureAlias+"internal/core/runtime/executor.go") {
		t.Fatalf("requirements.md 5.4 - the late pass must virtualize the reintroduced real path: %s", lateArgs)
	}
	if strings.Contains(lateArgs, fixtureRoot) {
		t.Fatalf("requirements.md 5.2/5.4 - no real root may survive on the reintroduced surface: %s", lateArgs)
	}
}

// TestBothPassesPublishOneAliasFromOnePinnedView is requirements.md 5.6 stated as a
// measurement at the composition boundary: the bundle's two outbound passes, driven
// through their OWN stage seams with the SAME pinned view, publish the identical value.
//
// The two passes are deliberately NOT handed the view the same way, because that is
// what production does: the early pass reads it from request.AttemptMeta.Workspace and
// the late pass from the public SDK context projection. What this pins is that one pin
// produces one alias, which is the property a live re-resolution could not offer.
func TestBothPassesPublishOneAliasFromOnePinnedView(t *testing.T) {
	t.Parallel()
	b := armedBundle(t, "enabled: true\nmode: rewrite\n")
	transform := lipfeature.Get(b.PlaneSet, lipfeature.PlaneAttemptTransforms)[0]
	hook := lipfeature.Get(b.PlaneSet, lipfeature.PlaneRequestPartHooks)[0]

	earlyCall, lateCall := pathCall(), pathCall()
	if _, err := transform.HandleAttempt(pinnedWorkspace(fixtureRoot), earlyCall,
		request.AttemptMeta{Workspace: lipworkspace.WorkspaceView{ProjectRoot: fixtureRoot}},
		request.Services{}); err != nil {
		t.Fatalf("HandleAttempt: %v", err)
	}
	if err := hook.HandleRequestParts(pinnedWorkspace(fixtureRoot), lateCall, sdkhooks.PartMeta{}); err != nil {
		t.Fatalf("HandleRequestParts: %v", err)
	}

	if got, want := arguments(t, lateCall), arguments(t, earlyCall); got != want {
		t.Fatalf("requirements.md 5.6 - one pinned view must yield one alias from both passes; early=%s late=%s", want, got)
	}
	if got := arguments(t, earlyCall); !strings.Contains(got, fixtureAlias) {
		t.Fatalf("requirements.md 1.1 - both passes must publish the fixed V1 alias for the pinned root: %s", got)
	}
	// Every retry, race participant, and failover candidate of one turn reads the same
	// projection, so repetition must be free and identical rather than stateful.
	retry := pathCall()
	if err := hook.HandleRequestParts(pinnedWorkspace(fixtureRoot), retry, sdkhooks.PartMeta{}); err != nil {
		t.Fatalf("HandleRequestParts: %v", err)
	}
	if got := arguments(t, retry); got != arguments(t, earlyCall) {
		t.Fatalf("requirements.md 5.6 - a second participant of the same turn must publish the identical alias")
	}
}

// TestTheBundlePublishesTheDeclaredMandatoryBound proves the operator's bound
// reaches the finalizer through the compiled policy rather than a default.
func TestTheBundlePublishesTheDeclaredMandatoryBound(t *testing.T) {
	t.Parallel()
	resolved := resolve(t, "enabled: true\nmode: rewrite\nmandatory_max_args_bytes: 65536\n")
	if resolved.ExpansionPolicy().MandatoryMaxArgsBytes != 65536 {
		t.Fatalf("policy bound = %d, want 65536", resolved.ExpansionPolicy().MandatoryMaxArgsBytes)
	}
	b := armedBundle(t, "enabled: true\nmode: rewrite\nmandatory_max_args_bytes: 65536\n")
	finalizer := lipfeature.Get(b.PlaneSet, lipfeature.PlaneToolCallFinalizers)[0]
	requirement, ok := finalizer.(toolcall.BufferingRequirement)
	if !ok {
		t.Fatalf("expansion finalizer does not publish a buffering requirement: %T", finalizer)
	}
	spec := requirement.ToolCallBufferingRequirement()
	if !spec.DeclaresMandatoryBound() {
		t.Fatal("expansion finalizer declared no mandatory bound")
	}
	if spec.MaxArgsBytes != 65536 {
		t.Fatalf("finalizer bound = %d, want 65536", spec.MaxArgsBytes)
	}
}

// TestTheBundleRefusesAnOutOfRangeBound keeps the generation-compilation refusal
// reachable through the bundle rather than only through Decode.
func TestTheBundleRefusesAnOutOfRangeBound(t *testing.T) {
	t.Parallel()
	if _, err := bundle.FeatureBundle(
		config.Resolved{Enabled: true, MandatoryMaxArgsBytes: -1},
	); err == nil {
		t.Fatal("expected an out-of-range declared bound to refuse the bundle")
	}
}

// mustYAML parses one operator subtree for the composition tests.
func mustYAML(t *testing.T, src string) yaml.Node {
	t.Helper()
	return node(t, src)
}
