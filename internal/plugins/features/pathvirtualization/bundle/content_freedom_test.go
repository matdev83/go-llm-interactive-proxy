package bundle_test

// This file pins requirements.md 7.7 at the bundle boundary: no path, alias,
// workspace tag, tool name, pointer, or argument byte the bundle handles may reach
// an observable surface.
//
// The bundle is the last place operator content is in scope, because it is the only
// place all three of it coexist - a compiled policy resolver that holds exact tool
// names and JSON Pointers, a project-root-derived alias, and complete tool-call
// arguments. Its contribution surface is deliberately dull for that reason:
//
//   - the only values it can refuse on are the SDK's plane-contribution errors and
//     the expansion constructor's bound refusal, and both carry bounded text;
//   - it registers NO reporter on any of the three components, so it declares no
//     metrics dimension of its own that a later edit could make content-bearing;
//   - and it emits no log line, no metric, and no error of its own.
//
// The identity strings below are exactly the content an observable surface would
// leak: a fixed low-cardinality label is safe, a workspace root or an alias is not.

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/bundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/config"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// contentMarkers are the shapes a leak would take. The reserved-namespace marker is
// spelled out because it is the most specific leak this feature could make: an
// alias in a metric label tells a reader both that the feature is active and which
// workspace it derived.
var contentMarkers = []string{
	"/.__lip_v1__",
	"/home/dev/projects",
	`\Users\dev`,
	"pkg/lipapi/call.go",
	"acme_internal_read_secrets",
	"file_path",
}

// TestNoObservableIdentityCarriesPathOrToolContent asserts that everything an
// operator could see about a participating component is a fixed literal. A
// low-cardinality identity is what a plugin suppression rule, an extension-stage
// failure log, and a diagnostics inventory all key on, so it is exactly where a
// composed-in value would leak.
func TestNoObservableIdentityCarriesPathOrToolContent(t *testing.T) {
	t.Parallel()
	b := bundleWithFixtureConfiguration(t)
	for _, identity := range []string{
		lipfeature.Get(b.PlaneSet, lipfeature.PlaneAttemptTransforms)[0].ID(),
		lipfeature.Get(b.PlaneSet, lipfeature.PlaneRequestPartHooks)[0].ID(),
		lipfeature.Get(b.PlaneSet, lipfeature.PlaneToolCallFinalizers)[0].ID(),
	} {
		for _, marker := range contentMarkers {
			if strings.Contains(identity, marker) {
				t.Fatalf("identity %q carries content %q", identity, marker)
			}
		}
	}
}

// TestTheBundleRegistersNoReporter pins that the composition seam declares no
// observability of its own. A reporter installed here would be a metric dimension
// this package controls, and the identity scan above only covers the fixed labels a
// component reports; a reporter receives the components' own bounded reports and
// could attach a content-bearing label later. requirements.md 9.3 owns the real
// counters, and it owns the content-freedom obligation with them.
//
// The assertion is structural because a reporter is an OPTION, not a return value:
// installing one changes nothing any behavioral test can observe from outside, so
// reading the construction site is the only way to make the rule falsifiable. Each
// of the three constructors takes a trailing variadic option list, and every one of
// them must be passed ZERO options.
func TestTheBundleRegistersNoReporter(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "bundle.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse bundle.go: %v", err)
	}
	matched := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		// Each of the three constructors takes a trailing VARIADIC option list, so
		// an exact argument count is what distinguishes "no options" from "options":
		// naming the option position is not enough, because the option itself is a
		// call expression rather than a variable the argument text would reveal.
		spec, ok := lookupConstructor(sel.Sel.Name)
		if !ok {
			return true
		}
		matched++
		if len(call.Args) != len(spec.want) {
			t.Errorf("%s is called with %d args, want exactly %d: this package must declare no observability of its own",
				sel.Sel.Name, len(call.Args), len(spec.want))
		}
		return true
	})
	if matched != len(bundleConstructors) {
		t.Fatalf("matched %d component constructor calls, want %d", matched, len(bundleConstructors))
	}
}

// TestAFullConfigurationReachesNoContentBearingError drives the complete configured
// surface - operator-declared profiles carrying a tool name and JSON Pointers - and
// asserts every value the bundle can return is free of it. The three refusals here
// are the whole set: the bound outside the SDK's range, a plane-contribution
// failure, and a successful build whose components must not echo configuration text.
func TestAFullConfigurationReachesNoContentBearingError(t *testing.T) {
	t.Parallel()
	const (
		toolName = "acme_internal_read_secrets"
		pointer  = "/payload/secret_path"
	)
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{
			name: "declared profile compiles",
			yaml: "enabled: true\nmode: rewrite\nschema_inference: true\ntool_profiles:\n  - names: [" + toolName +
				"]\n    arg_json_pointers: [" + pointer + "]\n",
		},
		{
			name: "declared profile conflicts",
			yaml: "enabled: true\nmode: rewrite\ntool_profiles:\n  - names: [" + toolName +
				"]\n    arg_json_pointers: [" + pointer + "]\n  - names: [" + toolName + "]\n    arg_json_pointers: [/path]\n",
		},
		{
			name: "unknown profile key",
			yaml: "enabled: true\nmode: rewrite\ntool_profiles:\n  - names: [" + toolName + "]\n    not_a_key: 1\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resolved, err := config.Decode(mustYAML(t, tc.yaml))
			b, bundleErr := bundle.FeatureBundle(resolved)
			if err != nil {
				assertNoContent(t, err.Error())
				return
			}
			if bundleErr != nil {
				assertNoContent(t, bundleErr.Error())
				return
			}
			// A successful build is equally content-free: nothing about the declared
			// tool name or pointer may be observable on the components it produced.
			for _, identity := range []string{
				lipfeature.Get(b.PlaneSet, lipfeature.PlaneAttemptTransforms)[0].ID(),
				lipfeature.Get(b.PlaneSet, lipfeature.PlaneRequestPartHooks)[0].ID(),
				lipfeature.Get(b.PlaneSet, lipfeature.PlaneToolCallFinalizers)[0].ID(),
			} {
				for _, marker := range append(contentMarkers, toolName, pointer) {
					if strings.Contains(identity, marker) {
						t.Fatalf("identity %q carries configured content %q", identity, marker)
					}
				}
			}
		})
	}
}

// TestTheComponentsPublishNoPayloadInTheirDecisions drives both outbound passes over
// a real path-bearing call and asserts the decision values are the inert
// no-payload shapes. The passes return a decision carrying no reference to the call
// and no error, so this is the observable-surface check for the hot path itself.
func TestTheComponentsPublishNoPayloadInTheirDecisions(t *testing.T) {
	t.Parallel()
	b := bundleWithFixtureConfiguration(t)
	transform := lipfeature.Get(b.PlaneSet, lipfeature.PlaneAttemptTransforms)[0]
	call := pathCall()
	decision, err := transform.HandleAttempt(context.Background(), call,
		request.AttemptMeta{Workspace: lipworkspace.WorkspaceView{ProjectRoot: fixtureRoot}},
		request.Services{})
	if err != nil {
		assertNoContent(t, err.Error())
		t.Fatal("the attempt transform must not return an error")
	}
	// The decision is the attempt continue kind with no payload and no reason code:
	// nothing an operator could read back about the call. requirements.md 5.5 puts
	// candidate choice outside this feature's reach, so the kind must not be the
	// exclusion kind, and an empty reason code is what keeps a failure log line free
	// of call detail.
	if decision.Kind != request.AttemptContinue {
		t.Fatalf("decision kind = %v, want AttemptContinue", decision.Kind)
	}
	if decision.ReasonCode != "" {
		t.Fatalf("decision carries a reason code %q; the pass must record a bounded report instead", decision.ReasonCode)
	}
	rendered, marshalErr := json.Marshal(decision)
	if marshalErr != nil {
		assertNoContent(t, marshalErr.Error())
		t.Fatal("decision must be marshalable")
	}
	assertNoContent(t, string(rendered))

	hook := lipfeature.Get(b.PlaneSet, lipfeature.PlaneRequestPartHooks)[0]
	if err := hook.HandleRequestParts(pinnedWorkspace(fixtureRoot), pathCall(), sdkhooks.PartMeta{}); err != nil {
		assertNoContent(t, err.Error())
		t.Fatal("the request-part hook must not return an error")
	}
}

// TestTheDisabledBundleExposesNoObservableSurface is the strongest form of the
// content-freedom obligation: an inert generation has no identity to inspect at
// all, so there is nothing an inventory could accidentally report.
func TestTheDisabledBundleExposesNoObservableSurface(t *testing.T) {
	t.Parallel()
	resolved, err := config.Decode(mustYAML(t, "enabled: false\nmode: rewrite\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := bundle.FeatureBundle(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !b.PlaneSet.IsZero() {
		t.Fatal("a disabled generation published an observable surface")
	}
	if len(b.Lifecycles) != 0 {
		t.Fatal("a disabled generation published lifecycles")
	}
}

func bundleWithFixtureConfiguration(t *testing.T) lipfeature.FeatureBundle {
	t.Helper()
	resolved, err := config.Decode(mustYAML(t, "enabled: true\nmode: rewrite\n"))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	b, err := bundle.FeatureBundle(resolved)
	if err != nil {
		t.Fatalf("FeatureBundle: %v", err)
	}
	return b
}

func assertNoContent(t *testing.T, message string) {
	t.Helper()
	for _, marker := range contentMarkers {
		if strings.Contains(message, marker) {
			t.Fatalf("observable message carries content %q: %s", marker, message)
		}
	}
}
