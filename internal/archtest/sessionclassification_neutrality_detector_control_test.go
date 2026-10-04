package archtest

// Task 10.3 non-vacuity controls for the neutrality sweep.
//
// A repo-wide "zero references" guard has exactly one fatal failure mode: a
// detector that finds nothing. That failure is invisible, because the guard
// passes and logs a clean table.
//
// These controls pin the detector against synthetic consumers that are
// type-checked through the SAME production path the sweep uses. The synthetic
// files are supplied through a packages overlay, so they are compiled and
// type-resolved for real while never touching the working tree: the archtest
// tree walkers read files with os.ReadFile and are overlay-blind, so writing a
// fixture into the repository would either leak into unrelated guards or need
// teardown a parallel budget test could observe.
//
// Each control is one consumer per overlay file so the reported/expected mapping
// is exact; a single combined file could report the union and hide a miss:
//
//   - reading SessionView.Classification MUST be reported;
//   - reaching it through a pointer MUST be reported;
//   - reaching it through local aliases of plain identifiers MUST be reported,
//     so a gate cannot be hidden by naming its receiver;
//   - calling IsCodingAgent on a value the consumer carries itself MUST be
//     reported;
//   - an unrelated struct member that is also called Classification MUST NOT be
//     reported. internal/plugins/features/reasoningpreservation ships exactly
//     that shape, so a detector that flagged it would force a permanent
//     false-positive exemption into the allowlist.
//
// The partition asserted is EXACT: every control file is accounted for in either
// the reported or the clean set. An all-empty result therefore fails.

import (
	"go/types"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// neutralControlHostPackage is the package the synthetic consumers are overlaid
// into. internal/agentfacts is small, type-checks cheaply, and deliberately
// carries no session-classification import of its own, so the overlay is the
// only thing that introduces one.
const neutralControlHostPackage = "./internal/agentfacts"

const neutralControlSessionHeader = `package agentfacts

import (
	"reflect"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

var _ = reflect.ValueOf
`

const neutralControlPlainHeader = `package agentfacts

`

// neutralControlSources are the synthetic consumers, one per overlay file. Every
// declaration is reachable from another declaration in the same file so nothing
// can become an unused no-op the detector would never see.
var neutralControlSources = map[string]string{
	"consumer_reads_field.go": `// consumerReadsField gates on the snapshot the way a real opted-in feature would.
func consumerReadsField(view session.SessionView) bool {
	return view.Classification.IsCodingAgent()
}
`,
	"consumer_reads_through_pointer.go": `// consumerReadsThroughPointer gates through a pointer receiver.
func consumerReadsThroughPointer(view *session.SessionView) bool {
	return view.Classification.Kind == session.KindCodingAgent
}
`,
	"consumer_reads_through_alias.go": `// consumerReadsThroughAlias hides the view behind two local aliases, so the gate
// cannot be discovered by naming its receiver.
func consumerReadsThroughAlias(view session.SessionView) bool {
	alias := view
	local := alias
	return local.Classification.Revision > 0
}
`,
	"consumer_calls_predicate.go": `// consumerCallsPredicate uses the contract predicate on a value the consumer
// carries itself, with no session view in scope at all.
func consumerCallsPredicate(snapshot session.Classification) bool {
	return snapshot.IsCodingAgent()
}
`,
	"unrelated_field_consumer.go": `// neutralControlCandidate is an unrelated struct whose own member is also called
// Classification.
type neutralControlCandidate struct {
	Name           string
	Classification int
}

// unrelatedFieldConsumer must NOT be reported. It imports no session contract at
// all, so a detector that flags it is reading names rather than types.
func unrelatedFieldConsumer(c neutralControlCandidate) int {
	return c.Classification
}
`,
}

// TestNeutralityDetectorReportsRealConsumersAndIgnoresUnrelatedFields is the
// detector control: it requires the EXACT reported/clean partition below, so a
// detector that silently reported nothing, or reported everything, fails.
func TestNeutralityDetectorReportsRealConsumersAndIgnoresUnrelatedFields(t *testing.T) {
	t.Parallel()

	byFile := neutralControlReasons(t)

	wantReported := []string{
		"consumer_calls_predicate.go",
		"consumer_reads_field.go",
		"consumer_reads_through_alias.go",
		"consumer_reads_through_pointer.go",
	}
	wantClean := []string{"unrelated_field_consumer.go"}

	for _, name := range wantReported {
		if len(byFile[name]) == 0 {
			t.Errorf("the detector reported nothing for %s; a real consumer would be invisible to the "+
				"neutrality guard", name)
		}
	}
	for _, name := range wantClean {
		if reasons := byFile[name]; len(reasons) != 0 {
			t.Errorf("the detector reported %s for %s; an unrelated same-named field must stay invisible, "+
				"otherwise reasoningpreservation would need a permanent false-positive exemption",
				strings.Join(reasons, ", "), name)
		}
	}

	// Every control file must be accounted for. Without this the test would
	// tolerate a detector that reported nothing for a filename it never saw.
	if len(byFile) != len(neutralControlSources) {
		t.Fatalf("the detector saw %d control files, want %d; a missing file makes the partition vacuous",
			len(byFile), len(neutralControlSources))
	}
	for _, name := range slices.Concat(wantReported, wantClean) {
		if _, ok := byFile[name]; !ok {
			t.Fatalf("the detector never produced a result for control file %q", name)
		}
	}

	// The reasons must be the RIGHT ones, so a detector that reported every file
	// for one accidental reason could not pass.
	if reasons := byFile["consumer_reads_through_pointer.go"]; !slices.Contains(reasons, "session-view-field") {
		t.Errorf("pointer consumer reasons = %v, want them to include session-view-field", reasons)
	}
	if reasons := byFile["consumer_calls_predicate.go"]; !slices.Contains(reasons, "classification-predicate") {
		t.Errorf("predicate consumer reasons = %v, want them to include classification-predicate", reasons)
	}
	if reasons := byFile["unrelated_field_consumer.go"]; len(reasons) != 0 {
		t.Errorf("unrelated field consumer reasons = %v, want none", reasons)
	}
	t.Logf("detector control: reported %v, clean %v", wantReported, wantClean)
}

// neutralControlReasons loads the host package with the synthetic consumers
// overlaid and returns the detector's reasons per control file name.
func neutralControlReasons(t *testing.T) map[string][]string {
	t.Helper()
	root := repoRoot(t)
	hostDir := filepath.Join(root, filepath.FromSlash("internal/agentfacts"))

	overlay := map[string][]byte{}
	for name, body := range neutralControlSources {
		header := neutralControlSessionHeader
		if name == "unrelated_field_consumer.go" || name == "consumer_unrelated_literal.go" {
			header = neutralControlPlainHeader
		}
		overlay[filepath.Join(hostDir, name)] = []byte(header + body)
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedSyntax | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedImports,
		Dir:     root,
		Overlay: overlay,
	}
	loaded, err := packages.Load(cfg, neutralControlHostPackage)
	if err != nil {
		t.Fatalf("load the host package with the control overlay: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("the host package loaded %d packages, want 1", len(loaded))
	}
	pkg := loaded[0]
	if len(pkg.Errors) > 0 {
		t.Fatalf("the host package does not type-check with the control overlay: %v", pkg.Errors[0])
	}

	var viewType *types.Named
	for _, imported := range pkg.Imports {
		if imported.PkgPath != sessionSDKPackage || imported.Types == nil {
			continue
		}
		if obj := imported.Types.Scope().Lookup("SessionView"); obj != nil {
			if named, ok := obj.Type().(*types.Named); ok {
				viewType = named
			}
		}
	}
	if viewType == nil {
		t.Fatalf("%s is not imported by the host package, so the field selector cannot be resolved",
			sessionSDKPackage)
	}

	byFile := map[string][]string{}
	for _, file := range pkg.Syntax {
		name := filepath.Base(pkg.Fset.Position(file.Pos()).Filename)
		if _, ok := neutralControlSources[name]; !ok {
			continue
		}
		byFile[name] = sessionClassificationReasonsIn(
			pkg, file, classificationPackageSet(), classificationSessionSurfaceSet(), viewType,
		)
	}
	return byFile
}

// TestNeutralityRoleAllowlistDoesNotSanctionNewSubpackages pins the allowlist's
// boundary. Directory roles match a directory's own files only, so a NEW
// subpackage under a classifier-owned directory earns no role until it is
// enumerated deliberately. A prefix match would auto-sanction exactly the path a
// classification-reading authorization or billing implementation would take, and
// the advisory-only sweep would not cover it either.
func TestNeutralityRoleAllowlistDoesNotSanctionNewSubpackages(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		file string
	}{
		{
			name: "a new subpackage under the standard featurehost binding",
			file: "internal/standardplugins/featurehost/sessionclassification/billing/billing.go",
		},
		{
			name: "a new subpackage under the wire evidence carrier",
			file: "internal/core/largebody/subpackage/authority.go",
		},
		{
			name: "a new subpackage under the feature classifier",
			file: "internal/plugins/features/sessionclassification/authority/gate.go",
		},
		{
			name: "a new subpackage under the SDK contract",
			file: "pkg/lipsdk/sessionclassification/extra/extra.go",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if roles := sessionClassificationRoleOf(tc.file); len(roles) != 0 {
				t.Fatalf("subpackage %q earned role(s) %v; a directory role must match only its own files, "+
					"or a new subpackage can read classification without deliberate review (requirements 1.7, 1.8)",
					tc.file, roles)
			}
		})
	}

	// The same directories' own files must still be sanctioned, or the tightening
	// above would have silently disabled the guard entirely.
	for _, file := range []string{
		"internal/standardplugins/featurehost/sessionclassification/coordinator.go",
		"internal/core/largebody/wire_runtime_facts.go",
		"internal/plugins/features/sessionclassification/classifier.go",
		"pkg/lipsdk/sessionclassification/contracts.go",
	} {
		if roles := sessionClassificationRoleOf(file); len(roles) == 0 {
			t.Fatalf("own-directory file %q lost its sanctioned role; the allowlist no longer covers it", file)
		}
	}
}
