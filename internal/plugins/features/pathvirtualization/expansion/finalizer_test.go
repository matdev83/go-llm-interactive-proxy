package expansion_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/expansion"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/schemainfer"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolcallrepair/repair"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	sdkworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// Spec: b-leg-path-virtualization Task 8.1, requirements.md 4.1, 4.3, 4.4, 4.7,
// 4.8, 4.9 and design.md section "7. Path Expansion Finalizer", whose eleven
// numbered steps this file walks one at a time.
//
// The fixture root is LONG on purpose. A short root is a supported root whose alias
// is correctly not shorter than the root itself, which leaves virtualization
// inactive by requirement 1.4 and would make every expectation below vacuous.

const (
	expansionProjectRoot = "/home/dev/workspaces/lip-path-virtualization-worktree"
	expansionOtherRoot   = "/home/dev/workspaces/lip-path-virtualization-main-checkout"

	expansionToolName = "custom_read"
)

// expansionFixture is one derived mapping plus the frozen facts every test needs.
type expansionFixture struct {
	mapping      pathvirtualization.Mapping
	virtualRoot  string
	workspaceTag string
	realRoot     string
	aliasPath    string
}

func newExpansionFixture(tb testing.TB) expansionFixture {
	tb.Helper()
	mapping, reason := pathvirtualization.DeriveMapping(expansionProjectRoot)
	if reason != pathvirtualization.SkipReasonNone {
		tb.Fatalf("derive %q: reason %v", expansionProjectRoot, reason)
	}
	if mapping.VirtualRoot == "" {
		tb.Fatal("the fixture root must derive an active alias (requirement 1.4)")
	}
	if len(mapping.WorkspaceTag) != 20 {
		tb.Fatalf("the frozen V1 tag must be 20 characters, got %d", len(mapping.WorkspaceTag))
	}
	return expansionFixture{
		mapping:      mapping,
		virtualRoot:  mapping.VirtualRoot,
		workspaceTag: mapping.WorkspaceTag,
		realRoot:     mapping.RealRoot,
		aliasPath:    mapping.VirtualRoot + "src/main.go",
	}
}

// expansionResolver builds a resolver whose exact operator profile claims one tool
// name with one argument selector. Nothing else is claimed, so every other tool name
// resolves to no selector and proves the pass-through path.
func expansionResolver(tb testing.TB, toolName string, pointers ...string) *pathvirtualization.Resolver {
	tb.Helper()
	compiled, reject := pathvirtualization.CompileProfiles([]pathvirtualization.ProfileInput{{
		Names:       []string{toolName},
		ArgPointers: pointers,
	}})
	if reject != pathvirtualization.SelectorRejectNone {
		tb.Fatalf("compile profiles: reject %v", reject)
	}
	resolver, reject := pathvirtualization.NewResolver(compiled, nil, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		tb.Fatalf("new resolver: reject %v", reject)
	}
	return resolver
}

// expansionMeta builds the finalizer metadata the runtime hands to every
// finalizer: the authoritative per-turn workspace view is the ONLY authority the
// pass reads for a project root.
func expansionMeta(projectRoot string) toolcall.Meta {
	return toolcall.Meta{Workspace: sdkworkspace.WorkspaceView{ProjectRoot: projectRoot}}
}

// expansionCall builds one completed tool call.
func expansionCall(toolName, argsJSON string) toolcall.CompletedCall {
	return toolcall.CompletedCall{ToolCallID: "call-1", ToolName: toolName, ArgsJSON: []byte(argsJSON)}
}

// newFinalizer builds the shipped finalizer with the default mandatory bound.
func newFinalizer(tb testing.TB, resolver *pathvirtualization.Resolver) *expansion.Finalizer {
	tb.Helper()
	fin, err := expansion.NewFinalizer(resolver, rewrite.ModeRewrite, expansion.Policy{})
	if err != nil {
		tb.Fatalf("NewFinalizer: %v", err)
	}
	return fin
}

// expansionReason decodes the bounded reason code a finalizer published.
func expansionReason(tb testing.TB, code string) expansion.Reason {
	tb.Helper()
	reason, ok := expansion.ParseReason(code)
	if !ok {
		tb.Fatalf("reason code %q is outside the closed vocabulary", code)
	}
	return reason
}

// TestExpansionFinalizerExpandsASelectedAlias walks design.md section 7 steps 1
// through 10 on the positive path: derive the mapping from the authoritative
// project root, resolve selectors against the exact tool name, parse the completed
// arguments, expand the alias inside its own string literal, and hand the
// assembler a valid rewritten document. Step 11 is the runtime's, and the
// assembler plus tool policy own it (requirement 4.3).
func TestExpansionFinalizerExpandsASelectedAlias(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))

	args := fmt.Sprintf(`{"file_path":%q,"content":"unchanged prose"}`, fixture.aliasPath)
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args),
		lipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionRewrite {
		t.Fatalf("action=%v want rewrite (reason %q)", res.Action, res.ReasonCode)
	}
	if res.ToolName != expansionToolName {
		t.Fatalf("tool name=%q want the call's own name %q", res.ToolName, expansionToolName)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonExpanded {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonExpanded)
	}
	if !json.Valid(res.ArgsJSON) {
		t.Fatalf("requirement 4.8 - published arguments are not valid JSON: %q", res.ArgsJSON)
	}
	want := fmt.Sprintf(`{"file_path":%q,"content":"unchanged prose"}`, fixture.realRoot+"/src/main.go")
	if string(res.ArgsJSON) != want {
		t.Fatalf("requirement 4.8 - published arguments are not the expanded splice:\n got %s\nwant %s",
			res.ArgsJSON, want)
	}
	if strings.Contains(string(res.ArgsJSON), ".__lip_v1__") {
		t.Fatalf("requirement 4.1 - the reserved alias survived expansion: %s", res.ArgsJSON)
	}
}

// TestExpansionFinalizerPreservesEveryUnselectedByte is requirement 4.8's other
// clause. The published document must be a byte splice: member order, duplicate
// keys, number spelling, escapes, whitespace, empty-versus-null presence, and the
// content field all survive unchanged, and nothing outside a selected leaf moves.
func TestExpansionFinalizerPreservesEveryUnselectedByte(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))

	args := `{ "z" : 1e400 , "file_path" : ` + strconv.Quote(fixture.aliasPath) + ` ,` +
		`"dup":"/kept","dup":"/also-kept",` +
		`"neg":-0,"frac":0.0,` +
		`"esc":"tab\there",` +
		`"empty":"","null":null,` +
		`"content":` + strconv.Quote(fixture.aliasPath) + ` }`

	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args),
		lipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionRewrite {
		t.Fatalf("action=%v want rewrite (reason %q)", res.Action, res.ReasonCode)
	}
	published := string(res.ArgsJSON)
	if !json.Valid(res.ArgsJSON) {
		t.Fatalf("requirement 4.8 - published arguments are not valid JSON: %s", published)
	}
	for _, kept := range []string{
		`"z" : 1e400`,
		`"file_path" : "`,
		`"dup":"/kept","dup":"/also-kept"`,
		`"neg":-0,"frac":0.0`,
		`"esc":"tab\there"`,
		`"empty":"","null":null`,
		`"content":"/.__lip_v1__`,
	} {
		if !strings.Contains(published, kept) {
			t.Fatalf("requirement 4.8 - published document lost unselected bytes %q: %s", kept, published)
		}
	}
	// Requirement 4.9: the content field is not path-bearing, so the alias inside
	// it is neither expanded nor rejected.
	if !strings.Contains(published, `"content":"`+fixture.virtualRoot) {
		t.Fatalf("requirement 4.9 - the content field must keep its own bytes: %s", published)
	}
	// The selected leaf is the ONLY changed byte: the real root appears exactly
	// once, at the selected location.
	if strings.Count(published, fixture.realRoot) != 1 {
		t.Fatalf("the real root must appear exactly once, in the selected leaf: %s", published)
	}
}

// TestExpansionFinalizerExpandsEveryElementOfASelectedArray proves the array-of-
// string shape is handled element by element, with a mixture of alias-bearing and
// ordinary values surviving intact.
func TestExpansionFinalizerExpandsEveryElementOfASelectedArray(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/paths"))

	args := fmt.Sprintf(`{"paths":[%q,"relative/third.go",%q]}`,
		fixture.aliasPath, fixture.virtualRoot+"pkg/other.go")
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args),
		lipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionRewrite {
		t.Fatalf("action=%v want rewrite (reason %q)", res.Action, res.ReasonCode)
	}
	want := fmt.Sprintf(`{"paths":[%q,"relative/third.go",%q]}`,
		fixture.realRoot+"/src/main.go", fixture.realRoot+"/pkg/other.go")
	if string(res.ArgsJSON) != want {
		t.Fatalf("published %s want %s", res.ArgsJSON, want)
	}
}

// TestExpansionFinalizerPassesThroughWhenThereIsNothingToExpand is requirement
// 4.7 plus design.md section 7 steps 2 and 3: a call with no virtual root, a tool
// no layer claims, an absent payload, and a usable root whose alias is not
// shorter all keep the pre-existing pass-through behavior with a bounded reason.
func TestExpansionFinalizerPassesThroughWhenThereIsNothingToExpand(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	shortRoot := "/srv/app"

	for _, tc := range []struct {
		name       string
		resolver   *pathvirtualization.Resolver
		toolName   string
		schema     []byte
		args       string
		project    string
		wantReason expansion.Reason
	}{
		{
			name:       "no_virtual_root_in_the_arguments",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   expansionToolName,
			args:       `{"file_path":"/home/dev/somewhere-else/src/main.go"}`,
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonNoAlias,
		},
		{
			name:       "relative_value_is_not_a_location",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   expansionToolName,
			args:       `{"file_path":"relative/src/main.go"}`,
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonNoAlias,
		},
		{
			name:       "no_selectors_for_the_exact_tool_name",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   "some_other_tool",
			args:       fmt.Sprintf(`{"file_path":%q}`, fixture.aliasPath),
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonNoSelectors,
		},
		{
			name:       "tool_name_lookup_is_byte_exact",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   "CUSTOM_READ",
			args:       fmt.Sprintf(`{"file_path":%q}`, fixture.aliasPath),
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonNoSelectors,
		},
		{
			name:       "arguments_absent",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   expansionToolName,
			args:       ``,
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonArgsAbsent,
		},
		{
			name:       "arguments_null",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   expansionToolName,
			args:       `null`,
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonArgsAbsent,
		},
		{
			name:       "arguments_are_not_an_object",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   expansionToolName,
			args:       `["` + fixture.aliasPath + `"]`,
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonPayloadNotObject,
		},
		{
			name:       "no_authoritative_project_root",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   expansionToolName,
			args:       `{"file_path":"/home/dev/somewhere-else/a.go"}`,
			project:    "",
			wantReason: expansion.ReasonRootUnusable,
		},
		{
			name:       "relative_project_root",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   expansionToolName,
			args:       `{"file_path":"/home/dev/somewhere-else/a.go"}`,
			project:    "relative/project",
			wantReason: expansion.ReasonRootUnusable,
		},
		{
			name:       "usable_root_whose_alias_is_not_shorter",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   expansionToolName,
			args:       `{"file_path":"/home/dev/somewhere-else/a.go"}`,
			project:    shortRoot,
			wantReason: expansion.ReasonMappingInactive,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fin := newFinalizer(t, tc.resolver)
			res, err := fin.Finalize(context.Background(), expansionCall(tc.toolName, tc.args),
				lipapi.ToolDef{Name: tc.toolName, Parameters: tc.schema}, nil, expansionMeta(tc.project))
			if err != nil {
				t.Fatalf("Finalize returned a Go error: %v", err)
			}
			if res.Action != toolcall.ActionPass {
				t.Fatalf("requirement 4.7 - action=%v want pass (reason %q)", res.Action, res.ReasonCode)
			}
			if res.ArgsJSON != nil {
				t.Fatalf("a pass must not carry a mutated document: %q", res.ArgsJSON)
			}
			if expansionReason(t, res.ReasonCode) != tc.wantReason {
				t.Fatalf("reason=%q want %q", res.ReasonCode, tc.wantReason)
			}
		})
	}
}

// TestExpansionFinalizerRejectsRecognizedReservedAliases is design.md section 7
// step 8 and requirement 4.4: a selected value that spells the fixed V1 reserved
// namespace but cannot be mapped unambiguously to the CURRENT workspace fails the
// whole tool call closed, and no alias ever reaches the client.
func TestExpansionFinalizerRejectsRecognizedReservedAliases(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	other, reason := pathvirtualization.DeriveMapping(expansionOtherRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("derive other root: reason %v", reason)
	}
	if other.WorkspaceTag == fixture.workspaceTag {
		t.Fatal("the two fixture roots must derive different workspace tags")
	}

	for _, tc := range []struct {
		name       string
		value      string
		project    string
		wantReason expansion.Reason
	}{
		{
			name:       "malformed_workspace_tag",
			value:      "/.__lip_v1__/w_short/src/main.go",
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonMalformedReservedAlias,
		},
		{
			name:       "tag_outside_the_base32_alphabet",
			value:      "/.__lip_v1__/w_0123456789abcdefgh01/src/main.go",
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonMalformedReservedAlias,
		},
		{
			name:       "stale_workspace_tag",
			value:      other.VirtualRoot + "src/main.go",
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonWorkspaceMismatch,
		},
		{
			name:       "incompatible_path_flavor",
			value:      `C:\.__lip_v1__\w_` + fixture.workspaceTag + `\src\main.go`,
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonWorkspaceMismatch,
		},
		{
			name:       "different_drive_of_the_same_flavor",
			value:      `D:\.__lip_v1__\w_` + fixture.workspaceTag + `\src\main.go`,
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonWorkspaceMismatch,
		},
		{
			name:       "active_tag_against_an_inactive_mapping",
			value:      fixture.aliasPath,
			project:    "/srv/app",
			wantReason: expansion.ReasonWorkspaceMismatch,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
			args := fmt.Sprintf(`{"file_path":%q}`, tc.value)
			res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args),
				lipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(tc.project))
			if err != nil {
				t.Fatalf("Finalize returned a Go error: %v", err)
			}
			if res.Action != toolcall.ActionReject {
				t.Fatalf("requirement 4.4 - action=%v want reject (reason %q)", res.Action, res.ReasonCode)
			}
			if expansionReason(t, res.ReasonCode) != tc.wantReason {
				t.Fatalf("reason=%q want %q", res.ReasonCode, tc.wantReason)
			}
			if res.ArgsJSON != nil {
				t.Fatalf("a rejection must not carry a mutated document: %q", res.ArgsJSON)
			}
		})
	}
}

// TestExpansionFinalizerRejectsAStaleAliasNeverAgainstTheNewRoot is requirement
// 6.5's security-critical half: a model-retained alias carrying another root's tag
// must never be expanded against the current one, so the published rejection must
// not contain either root.
func TestExpansionFinalizerRejectsAStaleAliasNeverAgainstTheNewRoot(t *testing.T) {
	t.Parallel()

	old, reason := pathvirtualization.DeriveMapping(expansionOtherRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("derive root A: reason %v", reason)
	}
	current := newExpansionFixture(t)
	if old.WorkspaceTag == current.workspaceTag {
		t.Fatal("roots A and B must derive different workspace tags")
	}
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))

	res, err := fin.Finalize(context.Background(),
		expansionCall(expansionToolName, fmt.Sprintf(`{"file_path":%q}`, old.VirtualRoot+"src/main.go")),
		lipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionReject {
		t.Fatalf("action=%v want reject", res.Action)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonWorkspaceMismatch {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonWorkspaceMismatch)
	}
	if res.ArgsJSON != nil {
		t.Fatalf("requirements.md 6.5 - a stale alias must never be expanded: %q", res.ArgsJSON)
	}
}

// TestExpansionFinalizerRefusesOneStaleLeafEvenBesideExpandableOnes proves the
// refusal is whole-document. Publishing the sibling expansion would hand the client
// a document where one path was decided and another was not.
func TestExpansionFinalizerRefusesOneStaleLeafEvenBesideExpandableOnes(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	other, reason := pathvirtualization.DeriveMapping(expansionOtherRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("derive other root: reason %v", reason)
	}
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/paths"))

	args := fmt.Sprintf(`{"paths":[%q,%q]}`, fixture.aliasPath, other.VirtualRoot+"src/main.go")
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args),
		lipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionReject {
		t.Fatalf("requirement 4.4 - action=%v want reject (reason %q)", res.Action, res.ReasonCode)
	}
	if res.ArgsJSON != nil {
		t.Fatalf("a refusal must publish nothing: %q", res.ArgsJSON)
	}
}

// TestExpansionFinalizerFailsClosedOnUnparseableAliasBearingArguments is design.md
// "Error Handling": "Malformed model JSON: existing repair/finalizer policy may
// repair first; a recognized applicable alias must never bypass required expansion
// and reach the client."
//
// The order declaration puts the shipped tool-call-repair finalizer first, so a
// repairable document arrives here already valid. This is the residual shape Task
// 7.3 characterized: repair declined the call for its own size policy, the
// document is still unparseable, and it still carries an alias. Passing it through
// would release the reserved namespace to the client, so it is refused closed.
func TestExpansionFinalizerFailsClosedOnUnparseableAliasBearingArguments(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))

	// The same document shape Task 7.3 pins: no closing quote, no closing brace,
	// and the reserved alias cut across stream fragments.
	malformed := `{"file_path":"` + fixture.aliasPath + `","content":"` + strings.Repeat("x", 1024)
	if json.Valid([]byte(malformed)) {
		t.Fatal("the fixture must be unparseable JSON")
	}
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, malformed),
		lipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionReject {
		t.Fatalf("requirements.md 4.4/8.3 - action=%v want reject (reason %q)", res.Action, res.ReasonCode)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonArgsUnparseable {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonArgsUnparseable)
	}
	if res.ArgsJSON != nil {
		t.Fatalf("a refusal must publish nothing: %q", res.ArgsJSON)
	}
}

// TestExpansionFinalizerMalformedTagOnAnUnparseableDocumentIsAlsoRefused keeps the
// unparseable path aligned with the parseable one: a reserved alias with an unusable
// tag is `malformed_reserved_alias` there and must not become a pass-through here.
func TestExpansionFinalizerMalformedTagOnAnUnparseableDocumentIsAlsoRefused(t *testing.T) {
	t.Parallel()

	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
	malformed := `{"file_path":"/.__lip_v1__/w_short/src/main.go","content":"xxxx`
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, malformed),
		lipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionReject {
		t.Fatalf("requirements.md 4.4 - action=%v want reject (reason %q)", res.Action, res.ReasonCode)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonArgsUnparseable {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonArgsUnparseable)
	}
}

// TestExpansionFinalizerPassesThroughUnparseableArgumentsWithNoAlias is the
// control that makes the rule above bounded. An unparseable document with no
// reserved alias carries nothing this feature must expand, so requirement 4.7's
// pass-through behavior is preserved and an unrelated feature's malformed call is
// not failed by this one.
func TestExpansionFinalizerPassesThroughUnparseableArgumentsWithNoAlias(t *testing.T) {
	t.Parallel()

	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
	for _, tc := range []struct {
		name       string
		args       string
		wantReason expansion.Reason
	}{
		{
			name:       "no_reserved_namespace_anywhere",
			args:       `{"file_path":"/home/dev/other/src/main.go","content":"xxxx`,
			wantReason: expansion.ReasonNoAlias,
		},
		{
			name:       "marker_is_not_a_complete_segment",
			args:       `{"file_path":"/home/dev/my.__lip_v1__/src/main.go","content":"xxxx`,
			wantReason: expansion.ReasonNoAlias,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if json.Valid([]byte(tc.args)) {
				t.Fatal("the fixture must be unparseable JSON")
			}
			res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, tc.args),
				lipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
			if err != nil {
				t.Fatalf("Finalize returned a Go error: %v", err)
			}
			if res.Action != toolcall.ActionPass {
				t.Fatalf("requirements.md 4.7 - action=%v want pass (reason %q)", res.Action, res.ReasonCode)
			}
			if expansionReason(t, res.ReasonCode) != tc.wantReason {
				t.Fatalf("reason=%q want %q", res.ReasonCode, tc.wantReason)
			}
			if res.ArgsJSON != nil {
				t.Fatalf("no decision may publish a mutated document: %q", res.ArgsJSON)
			}
		})
	}
}

// TestExpansionFinalizerUnparseableReservedNamespaceInContentIsStillRefused pins
// the ACCEPTED FAIL-CLOSED RESIDUAL of the unparseable-document rule, and it is
// deliberate rather than accidental.
//
// To decide anything about an unreadable document the pass must scan raw bytes,
// because there is no structure left to read. That scan sees the whole payload, so
// a reserved alias spelled inside a `content`/`patch`/`script` field of an
// UNPARSEABLE document is refused even though requirement 4.9 forbids expanding
// such a field. Two things bound the trade:
//
//   - nothing is expanded, so requirement 4.9's prohibition is never violated; the
//     call is refused, not rewritten;
//   - the fixed V1 reserved namespace can never be a legitimate real path, because
//     requirement 1.8 refuses to derive a mapping from a root spelled inside it, so
//     the byte pattern this rule keys on is unreachable in correct client data.
//
// The alternative - expanding nothing and releasing - is the exact outcome
// design.md "Error Handling" forbids, so the refusal is the safe direction. The
// parseable counterpart is pinned separately: a document that DOES parse keeps its
// content field byte-for-byte and is never refused for it.
func TestExpansionFinalizerUnparseableReservedNamespaceInContentIsStillRefused(t *testing.T) {
	t.Parallel()

	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
	prose := `{"file_path":"/home/dev/other/a.go","content":"see /.__lip_v1__/w_0123456789abcdefghij/ for details`
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, prose),
		lipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionReject {
		t.Fatalf("action=%v want reject (reason %q)", res.Action, res.ReasonCode)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonArgsUnparseable {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonArgsUnparseable)
	}
	if res.ArgsJSON != nil {
		t.Fatalf("a refusal must publish nothing: %q", res.ArgsJSON)
	}
}

// TestExpansionFinalizerUnparseableAliasForAnUnprovedToolStillPassesThrough is the
// applicability boundary of the unparseable-document rule. "A recognized
// APPLICABLE alias" is design.md's own wording, and a call is one path
// virtualization applies to exactly when its tool's policy names at least one
// argument location. That is answerable without parsing the document, because
// selector resolution reads the profile layer and the declared schema, never the
// payload. A tool no layer claims therefore keeps the pre-existing pass-through
// behavior of requirements.md 3.5 and 4.7 even when its unreadable arguments happen
// to contain the reserved marker.
func TestExpansionFinalizerUnparseableAliasForAnUnprovedToolStillPassesThrough(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
	malformed := `{"file_path":"` + fixture.aliasPath + `","content":"xxxx`

	res, err := fin.Finalize(context.Background(), expansionCall("unclaimed_tool", malformed),
		lipapi.ToolDef{Name: "unclaimed_tool"}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionPass {
		t.Fatalf("requirements.md 3.5/4.7 - action=%v want pass (reason %q)", res.Action, res.ReasonCode)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonNoSelectors {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonNoSelectors)
	}
	if res.ArgsJSON != nil {
		t.Fatalf("a pass must not publish a mutated document: %q", res.ArgsJSON)
	}
}

// TestExpansionFinalizerUnparseableAliasIsRefusedEvenWithoutADeclaredSchema keeps
// the refusal from depending on schema inference. An explicit profile is one way
// to prove a location path-bearing; it is not the only requirement for the tool to
// be one this feature applies to.
func TestExpansionFinalizerUnparseableAliasIsRefusedEvenWithoutADeclaredSchema(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
	malformed := `{"file_path":"` + fixture.aliasPath + `","content":"xxxx`

	// No catalog entry at all: the assembler would still resolve the tool's
	// selectors from the exact-name profile layer.
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, malformed),
		lipapi.ToolDef{}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionReject {
		t.Fatalf("requirements.md 4.4/8.3 - action=%v want reject (reason %q)", res.Action, res.ReasonCode)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonArgsUnparseable {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonArgsUnparseable)
	}
}

// TestExpansionFinalizerDeclaresMandatoryBuffering is design.md section 6: the
// pass declares OverflowReject with a 1 MiB default, and requirement 7.5 makes an
// out-of-range configured bound a generation-compilation failure rather than a
// silently degraded declaration.
func TestExpansionFinalizerDeclaresMandatoryBuffering(t *testing.T) {
	t.Parallel()

	t.Run("default_bound_and_reject_policy", func(t *testing.T) {
		t.Parallel()
		fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
		var declared toolcall.Finalizer = fin
		req, ok := declared.(toolcall.BufferingRequirement)
		if !ok {
			t.Fatal("the expansion finalizer must declare the shipped mandatory buffering capability")
		}
		spec := req.ToolCallBufferingRequirement()
		if spec.Overflow != toolcall.OverflowReject {
			t.Fatalf("design.md section 6 - overflow policy=%q want %q", spec.Overflow, toolcall.OverflowReject)
		}
		if spec.MaxArgsBytes != toolcall.DefaultMandatoryMaxArgsBytes {
			t.Fatalf("default bound=%d want %d", spec.MaxArgsBytes, toolcall.DefaultMandatoryMaxArgsBytes)
		}
		if !spec.DeclaresMandatoryBound() {
			t.Fatalf("the default declaration must be well formed: %+v", spec)
		}
	})

	t.Run("the_configurable_range_is_accepted", func(t *testing.T) {
		t.Parallel()
		for _, bound := range []int{
			toolcall.MinMandatoryMaxArgsBytes,
			toolcall.DefaultMandatoryMaxArgsBytes,
			toolcall.MaxMandatoryMaxArgsBytes,
		} {
			fin, err := expansion.NewFinalizer(expansionResolver(t, expansionToolName, "/file_path"),
				rewrite.ModeRewrite, expansion.Policy{MandatoryMaxArgsBytes: bound})
			if err != nil {
				t.Fatalf("bound %d must compile: %v", bound, err)
			}
			if got := fin.ToolCallBufferingRequirement().MaxArgsBytes; got != bound {
				t.Fatalf("declared bound=%d want %d", got, bound)
			}
		}
	})

	t.Run("an_out_of_range_bound_fails_generation_compilation", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name  string
			bound int
		}{
			{name: "negative", bound: -1},
			{name: "below_the_floor", bound: toolcall.MinMandatoryMaxArgsBytes - 1},
			{name: "above_the_ceiling", bound: toolcall.MaxMandatoryMaxArgsBytes + 1},
			{name: "wildly_out_of_range", bound: 1 << 40},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				spec := toolcall.BufferingSpec{MaxArgsBytes: tc.bound, Overflow: toolcall.OverflowReject}
				if err := spec.Validate(); err == nil {
					t.Fatalf("the boundary fixture must be out of range: %d", tc.bound)
				}
				fin, err := expansion.NewFinalizer(expansionResolver(t, expansionToolName, "/file_path"),
					rewrite.ModeRewrite, expansion.Policy{MandatoryMaxArgsBytes: tc.bound})
				if err == nil {
					t.Fatal("requirement 7.5 - an unsupported mandatory bound must fail generation compilation")
				}
				if fin != nil {
					t.Fatal("a failed compilation must not return a usable finalizer")
				}
				if !strings.Contains(err.Error(), "path_virtualization") {
					t.Fatalf("the failure must name the feature: %v", err)
				}
			})
		}
	})
}

// TestExpansionFinalizerOrdersAboveToolCallRepair is Task 7.3's hand-off
// evidence. requirements.md 8.4 requires mandatory expansion to receive valid
// completed JSON, and the whole mechanism is one number: the shipped
// tool-call-repair finalizer's DefaultFinalizerOrder plus one. This test imports
// the shipped repair feature to read that constant, exactly as Task 7.3's
// composition evidence does.
func TestExpansionFinalizerOrdersAboveToolCallRepair(t *testing.T) {
	t.Parallel()

	if expansion.FinalizerOrder != 41 {
		t.Fatalf("the declared order changed: %d want 41", expansion.FinalizerOrder)
	}
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
	if fin.Order() != expansion.FinalizerOrder {
		t.Fatalf("Order()=%d want the declared %d", fin.Order(), expansion.FinalizerOrder)
	}
	if fin.Order() <= repair.DefaultFinalizerOrder {
		t.Fatalf("requirements.md 8.4 - the expansion order must sit strictly above tool-call repair: %d <= %d",
			fin.Order(), repair.DefaultFinalizerOrder)
	}
}

// TestExpansionFinalizerAuditModeMeasuresWithoutExpanding is requirement 7.3 for
// the inbound direction: audit runs the identical detection and publishes nothing,
// so the client sees what the rewrite would have seen and the operator sees the
// same numbers the rewrite would have produced.
func TestExpansionFinalizerAuditModeMeasuresWithoutExpanding(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	var reports []expansion.Report
	fin, err := expansion.NewFinalizer(expansionResolver(t, expansionToolName, "/file_path"),
		rewrite.ModeAudit, expansion.Policy{},
		expansion.WithReporter(func(r expansion.Report) { reports = append(reports, r) }))
	if err != nil {
		t.Fatalf("NewFinalizer: %v", err)
	}
	args := fmt.Sprintf(`{"file_path":%q}`, fixture.aliasPath)
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args),
		lipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionPass {
		t.Fatalf("requirement 7.3 - audit mode must publish nothing: action=%v", res.Action)
	}
	if res.ArgsJSON != nil {
		t.Fatalf("requirement 7.3 - audit mode must publish nothing: %q", res.ArgsJSON)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonAuditMode {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonAuditMode)
	}
	if len(reports) != 1 {
		t.Fatalf("reports=%d want 1", len(reports))
	}
	if reports[0].Stats.Eligible != 1 || reports[0].Stats.Rewritten != 1 {
		t.Fatalf("requirement 7.3 - audit must measure the rewrite's own numbers: %+v", reports[0].Stats)
	}
	// Requirement 9.5's savings are the OUTBOUND direction's number. The inbound
	// expansion necessarily grows the value it replaces, so the measured totals must be
	// the exact decoded lengths of the alias it read and the real path it would publish.
	expandedValue := fixture.realRoot + "/src/main.go"
	if reports[0].Stats.BytesBefore != len(fixture.aliasPath) {
		t.Fatalf("BytesBefore=%d want the alias length %d", reports[0].Stats.BytesBefore, len(fixture.aliasPath))
	}
	if reports[0].Stats.BytesAfter != len(expandedValue) {
		t.Fatalf("BytesAfter=%d want the expanded length %d", reports[0].Stats.BytesAfter, len(expandedValue))
	}

	// The rewrite-mode pass over the identical bytes must report identical
	// statistics, which is what makes an audit number a claim about the rewrite.
	rewriteReports := make([]expansion.Report, 0, 1)
	rewriteFin, err := expansion.NewFinalizer(expansionResolver(t, expansionToolName, "/file_path"),
		rewrite.ModeRewrite, expansion.Policy{},
		expansion.WithReporter(func(r expansion.Report) { rewriteReports = append(rewriteReports, r) }))
	if err != nil {
		t.Fatalf("NewFinalizer: %v", err)
	}
	rewriteRes, err := rewriteFin.Finalize(context.Background(), expansionCall(expansionToolName, args),
		lipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if rewriteRes.Action != toolcall.ActionRewrite {
		t.Fatalf("rewrite mode action=%v want rewrite", rewriteRes.Action)
	}
	if !sameStats(reports[0].Stats, rewriteReports[0].Stats) {
		t.Fatalf("requirement 7.3 - audit and rewrite must report the same statistics:\naudit  %+v\nrewrite %+v",
			reports[0].Stats, rewriteReports[0].Stats)
	}
}

// sameStats compares two content-free statistics values field for field, including the
// skip tallies, which a Go struct comparison cannot do because one holds a slice.
func sameStats(a, b rewrite.Stats) bool {
	if a.Eligible != b.Eligible || a.Rewritten != b.Rewritten ||
		a.BytesBefore != b.BytesBefore || a.BytesAfter != b.BytesAfter {
		return false
	}
	if len(a.Skips) != len(b.Skips) {
		return false
	}
	for i := range a.Skips {
		if a.Skips[i] != b.Skips[i] {
			return false
		}
	}
	return true
}

// TestExpansionFinalizerResolvesSelectorsAgainstTheDeclaredSchema walks design.md
// section 7 step 2 with the optional inference step enabled. Without a declared
// schema the tool resolves to no selector at all; with one, the same exact tool
// name resolves through inference and the alias is expanded.
func TestExpansionFinalizerResolvesSelectorsAgainstTheDeclaredSchema(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	inferrer, reject := schemainfer.New(schemainfer.DefaultPathKeys())
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("new inferrer: reject %v", reject)
	}
	inference, reject := pathvirtualization.NewResolver(nil, nil, inferrer)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("new resolver: reject %v", reject)
	}
	fin, err := expansion.NewFinalizer(inference, rewrite.ModeRewrite, expansion.Policy{})
	if err != nil {
		t.Fatalf("NewFinalizer: %v", err)
	}
	const schema = `{"type":"object","properties":{"path":{"type":"string"}}}`
	args := fmt.Sprintf(`{"path":%q}`, fixture.aliasPath)

	withoutSchema, err := fin.Finalize(context.Background(), expansionCall("inferred_tool", args),
		lipapi.ToolDef{Name: "inferred_tool"}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if withoutSchema.Action != toolcall.ActionPass {
		t.Fatalf("requirement 3.5 - an undeclared tool must resolve to no selector: action=%v",
			withoutSchema.Action)
	}

	withSchema, err := fin.Finalize(context.Background(), expansionCall("inferred_tool", args),
		lipapi.ToolDef{Name: "inferred_tool", Parameters: []byte(schema)}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if withSchema.Action != toolcall.ActionRewrite {
		t.Fatalf("the declared schema must resolve a selector: action=%v reason=%q",
			withSchema.Action, withSchema.ReasonCode)
	}

	// A near-miss tool name never receives another tool's schema.
	nearMiss, err := fin.Finalize(context.Background(), expansionCall("inferred_tool ", args),
		lipapi.ToolDef{Name: "inferred_tool", Parameters: []byte(schema)}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if nearMiss.Action != toolcall.ActionPass {
		t.Fatalf("requirement 3.6 - tool-name lookup must be byte-exact: action=%v", nearMiss.Action)
	}
}

// TestExpansionFinalizerReportsAreContentFree proves nothing a caller can observe
// about one decision carries a path, an alias, a workspace tag, a tool name, a
// pointer, or argument bytes.
func TestExpansionFinalizerReportsAreContentFree(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	other, reason := pathvirtualization.DeriveMapping(expansionOtherRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("derive other: reason %v", reason)
	}

	for _, args := range []string{
		fmt.Sprintf(`{"file_path":%q}`, fixture.aliasPath),
		fmt.Sprintf(`{"file_path":%q}`, other.VirtualRoot+"secret/payload.go"),
		`{"file_path":"/.__lip_v1__/w_short/secret/payload.go"}`,
		`{"file_path":"/home/dev/other/` + strings.Repeat("x", 64) + `.go"}`,
	} {
		var reports []expansion.Report
		fin, err := expansion.NewFinalizer(expansionResolver(t, expansionToolName, "/file_path"),
			rewrite.ModeRewrite, expansion.Policy{},
			expansion.WithReporter(func(r expansion.Report) { reports = append(reports, r) }))
		if err != nil {
			t.Fatalf("NewFinalizer: %v", err)
		}
		if _, ferr := fin.Finalize(context.Background(), expansionCall(expansionToolName, args),
			lipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot)); ferr != nil {
			t.Fatalf("Finalize returned a Go error: %v", ferr)
		}
		if len(reports) != 1 {
			t.Fatalf("reports=%d want 1", len(reports))
		}
		rendered := fmt.Sprintf("%v|%v|%v|%v", reports[0].Outcome, reports[0].Reason,
			reports[0].RootReason, reports[0].Stats)
		for _, forbidden := range []string{
			expansionProjectRoot, expansionOtherRoot, fixture.virtualRoot, other.VirtualRoot,
			fixture.workspaceTag, other.WorkspaceTag, "w_", ".__lip_v1__",
			expansionToolName, "file_path", "payload.go", "xxxx",
		} {
			if strings.Contains(rendered, forbidden) {
				t.Fatalf("requirements.md 7.7 - a report leaked %q: %s", forbidden, rendered)
			}
		}
	}
}

// TestExpansionFinalizerReasonVocabularyIsClosedAndContentFree pins the bounded
// label set both as a metric dimension and as the string the assembler turns into
// a client-facing refusal, and proves the reason is a valid
// toolcall.Result.ReasonCode rather than free-form text.
func TestExpansionFinalizerReasonVocabularyIsClosedAndContentFree(t *testing.T) {
	t.Parallel()

	known := []struct {
		reason expansion.Reason
		want   string
	}{
		{reason: expansion.ReasonNone, want: ""},
		{reason: expansion.ReasonExpanded, want: "expanded"},
		{reason: expansion.ReasonAuditMode, want: "audit_mode"},
		{reason: expansion.ReasonNoAlias, want: "no_alias"},
		{reason: expansion.ReasonNoSelectors, want: "no_selectors"},
		{reason: expansion.ReasonArgsAbsent, want: "args_absent"},
		{reason: expansion.ReasonPayloadNotObject, want: "payload_not_object"},
		{reason: expansion.ReasonRootUnusable, want: "root_unusable"},
		{reason: expansion.ReasonMappingInactive, want: "mapping_inactive"},
		{reason: expansion.ReasonArgsUnparseable, want: "args_unparseable"},
		{reason: expansion.ReasonMalformedReservedAlias, want: "malformed_reserved_alias"},
		{reason: expansion.ReasonWorkspaceMismatch, want: "workspace_mismatch"},
		{reason: expansion.ReasonInvalidRewrite, want: "invalid_rewrite"},
	}
	for _, tc := range known {
		if got := tc.reason.String(); got != tc.want {
			t.Fatalf("Reason(%d).String()=%q want %q", int(tc.reason), got, tc.want)
		}
	}
	if got := expansion.Reason(200).String(); got != "unknown" {
		t.Fatalf("an undefined reason must degrade to a bounded label, got %q", got)
	}
	for _, tc := range known {
		if tc.reason == expansion.ReasonNone {
			continue
		}
		if !strings.HasPrefix(tc.want, "reason_") && strings.ContainsAny(tc.want, "/\\.:") {
			t.Fatalf("reason label %q is not a safe bounded token", tc.want)
		}
		for _, forbidden := range []string{".__lip_v1__", "w_", "/home", `\Users`, "C:", "file_path"} {
			if strings.Contains(tc.want, forbidden) {
				t.Fatalf("reason label %q leaks %q", tc.want, forbidden)
			}
		}
	}
}

// TestExpansionFinalizerNilAndZeroValuesAreInactive keeps the shipped type safe
// in the shapes a composition root can produce: a nil finalizer, a nil resolver,
// and a nil reporter must all behave as the required "no policy, nothing proven"
// answers rather than panicking or guessing.
func TestExpansionFinalizerNilAndZeroValuesAreInactive(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	args := fmt.Sprintf(`{"file_path":%q}`, fixture.aliasPath)

	var nilFin *expansion.Finalizer
	res, err := nilFin.Finalize(context.Background(), expansionCall(expansionToolName, args),
		lipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("a nil finalizer must not surface a Go error: %v", err)
	}
	if res.Action != toolcall.ActionPass {
		t.Fatalf("a nil finalizer must pass through: action=%v", res.Action)
	}
	if nilFin.ID() != expansion.FinalizerID {
		t.Fatalf("a nil finalizer must still report its stable identity, got %q", nilFin.ID())
	}
	if nilFin.Order() != expansion.FinalizerOrder {
		t.Fatalf("a nil finalizer must still report its declared order, got %d", nilFin.Order())
	}
	if spec := nilFin.ToolCallBufferingRequirement(); !spec.DeclaresMandatoryBound() {
		t.Fatalf("a nil finalizer must still declare the mandatory bound, got %+v", spec)
	}

	fin, err := expansion.NewFinalizer(nil, rewrite.ModeRewrite, expansion.Policy{})
	if err != nil {
		t.Fatalf("a nil resolver must compile: %v", err)
	}
	res, err = fin.Finalize(context.Background(), expansionCall(expansionToolName, args),
		lipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionPass || expansionReason(t, res.ReasonCode) != expansion.ReasonNoSelectors {
		t.Fatalf("a nil resolver must select nothing: action=%v reason=%q", res.Action, res.ReasonCode)
	}
}

// TestExpansionFinalizerIsDeterministic re-runs the same decision many times and
// requires byte-identical results and reports, so nothing in the pass depends on
// map iteration order or accumulated state.
func TestExpansionFinalizerIsDeterministic(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	args := fmt.Sprintf(`{"file_path":%q,"paths":[%q],"content":"kept"}`,
		fixture.aliasPath, fixture.virtualRoot+"pkg/other.go")
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path", "/paths"))
	meta := expansionMeta(expansionProjectRoot)
	tool := lipapi.ToolDef{Name: expansionToolName}

	first, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args), tool, nil, meta)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	for range 5 {
		again, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args), tool, nil, meta)
		if err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		if again.Action != first.Action || again.ReasonCode != first.ReasonCode ||
			again.ToolName != first.ToolName || !bytes.Equal(again.ArgsJSON, first.ArgsJSON) {
			t.Fatalf("a repeated decision differed:\nfirst  %+v\nsecond %+v", first, again)
		}
	}
}
