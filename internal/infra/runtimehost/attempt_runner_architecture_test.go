package runtimehost

// Task 6.3 permanent architecture enforcement: attemptRunner is the single
// owner of the one-attempt transaction, Coordinator delegates to it instead
// of implementing detailed reload stage branches, and the runner never
// touches AttemptGate/history/status ownership. Enforcement reuses Task 6.2
// provenance/AST helpers — not a second multi-thousand-line analyzer and not
// an exact-name blacklist.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAttemptRunner_SingleOwnerDeclaration proves exactly one production
// attemptRunner struct declaration, and it lives in attempt_runner.go.
func TestAttemptRunner_SingleOwnerDeclaration(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	files := parseProductionRuntimehostFiles(t, fset)
	violations := analyzeRunnerOwnership(files)
	if len(violations) > 0 {
		t.Fatalf("attemptRunner ownership violations:\n%s", strings.Join(violations, "\n"))
	}
}

// TestAttemptRunner_CoordinatorSingleRunnerFieldAndCaller proves Coordinator
// declares exactly one runner field, exactly one construction call site, and
// that (*attemptRunner).Run is called only from Coordinator.Reload.
func TestAttemptRunner_CoordinatorSingleRunnerFieldAndCaller(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	files := parseProductionRuntimehostFiles(t, fset)

	file := files["coordinator.go"]
	if file == nil {
		t.Fatal("coordinator.go missing from production scan")
	}
	coord := findTypeSpec(file, "Coordinator")
	if coord == nil {
		t.Fatal("Coordinator type missing")
	}
	st, ok := coord.Type.(*ast.StructType)
	if !ok || st.Fields == nil {
		t.Fatal("Coordinator is not a struct")
	}
	runnerFields := 0
	for _, f := range st.Fields.List {
		if typeExprString(f.Type) != "*attemptRunner" {
			continue
		}
		for _, n := range f.Names {
			if n.Name != "runner" {
				t.Fatalf("Coordinator runner field must be named runner; got %q", n.Name)
			}
			runnerFields++
		}
	}
	if runnerFields != 1 {
		t.Fatalf("want exactly one Coordinator runner *attemptRunner field; got %d", runnerFields)
	}

	if got := analyzeRunnerConstructorGraph(files); len(got) > 0 {
		t.Fatalf("newAttemptRunner constructor graph violations:\n%s", strings.Join(got, "\n"))
	}

	sites, methodVals := findAttemptRunnerRunSites(files)
	if len(sites) == 0 {
		t.Fatal("expected at least one (*attemptRunner).Run call site")
	}
	for _, s := range sites {
		if s.file != "coordinator.go" || !strings.HasSuffix(s.fn, "Coordinator.Reload") {
			t.Fatalf("unexpected Run caller outside Coordinator.Reload: %s:%s", s.file, s.fn)
		}
	}
	if len(methodVals) > 0 {
		t.Fatalf("method-value Run aliases forbidden:\n%s", strings.Join(methodVals, "\n"))
	}
}

// TestAttemptRunner_CoordinatorNoLongerExecutesReloadStagesDirectly proves
// Coordinator no longer calls Source.ReadStable, Loader.LoadEffective,
// classify, CandidateCompiler.Compile, Manager.PrepareRequestPlane, or
// Manager.Publish directly (or via local aliases/wrappers) for reload
// execution (moved onto the runner).
func TestAttemptRunner_CoordinatorNoLongerExecutesReloadStagesDirectly(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	files := parseProductionRuntimehostFiles(t, fset)
	if got := forbiddenCoordinatorReloadExecutionCalls(files); len(got) > 0 {
		t.Fatalf("Coordinator must not directly execute reload stages:\n%s", strings.Join(got, "\n"))
	}
}

// TestAttemptRunner_NoGateOwnershipOrCoordinatorMutation proves attempt_runner.go
// never references attemptGate/attemptLease types or gate transition methods,
// and never calls recordTerminal or references Coordinator-owned state fields.
func TestAttemptRunner_NoGateOwnershipOrCoordinatorMutation(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	files := parseProductionRuntimehostFiles(t, fset)
	runnerFile := files["attempt_runner.go"]
	if runnerFile == nil {
		t.Fatal("attempt_runner.go missing from production scan")
	}
	if got := scanRunnerFileForForbiddenOwnership(runnerFile); len(got) > 0 {
		t.Fatalf("attempt_runner.go ownership violations:\n%s", strings.Join(got, "\n"))
	}
}

// TestAttemptRunner_NoDuplicateGenerationOrHookBag proves there is no second
// plane/generation lifecycle bag type and no generic hook-registry type
// introduced alongside the runner.
func TestAttemptRunner_NoDuplicateGenerationOrHookBag(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	files := parseProductionRuntimehostFiles(t, fset)
	for path, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil {
					continue
				}
				name := ts.Name.Name
				lower := strings.ToLower(name)
				if strings.Contains(lower, "hook") && strings.Contains(lower, "registry") {
					t.Fatalf("%s: unexpected generic hook registry type %q", path, name)
				}
				if name != "Generation" && strings.Contains(lower, "generation") && strings.Contains(lower, "bag") {
					t.Fatalf("%s: unexpected duplicate generation lifecycle bag type %q", path, name)
				}
			}
		}
	}
}

// TestAttemptRunner_NoWallClockSyncInRunnerTests rejects time.Sleep/After/
// timers/tickers and runtime.Gosched in attempt_runner*_test.go. Barrier
// deadlock guards must use context.WithTimeout; past-deadline contexts are OK.
func TestAttemptRunner_NoWallClockSyncInRunnerTests(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var scanned int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}
		if !strings.HasPrefix(name, "attempt_runner") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			pos := fset.Position(call.Pos())
			switch pkg.Name {
			case "time":
				switch sel.Sel.Name {
				case "Sleep", "After", "NewTicker", "Tick", "AfterFunc", "NewTimer":
					t.Fatalf("attempt_runner suite must not use time.%s for sync at %s:%d (use barriers/channels; context.WithTimeout only as post-barrier deadlock guard)",
						sel.Sel.Name, filepath.Base(pos.Filename), pos.Line)
				}
			case "runtime":
				if sel.Sel.Name == "Gosched" {
					t.Fatalf("attempt_runner suite must not use runtime.Gosched at %s:%d",
						filepath.Base(pos.Filename), pos.Line)
				}
			}
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("expected attempt_runner*_test.go files to scan")
	}
}

// --- synthetic evasion / negative-control fixtures ---

func TestAttemptRunnerOwnershipScanner_SyntheticEvasions(t *testing.T) {
	t.Parallel()

	t.Run("rejects_second_attemptRunner_declaration", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
`,
			"decoy.go": `
package runtimehost
type attemptRunner2 = attemptRunner
`,
			"other.go": `
package runtimehost
type attemptRunner struct{ x int }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerOwnership(files)
		if !violationContains(got, "attemptRunner") {
			t.Fatalf("expected duplicate attemptRunner rejection; got %v", got)
		}
	})

	t.Run("accepts_canonical_single_owner", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerOwnership(files)
		if len(got) > 0 {
			t.Fatalf("canonical single owner must be accepted; got %v", got)
		}
	})

	t.Run("rejects_type_alias_and_defined_attemptRunner", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
`,
			"alias.go": `
package runtimehost
type other = attemptRunner
type otherDefined attemptRunner
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerOwnership(files)
		if !violationContains(got, "other") || !violationContains(got, "otherDefined") {
			t.Fatalf("expected attemptRunner alias/defined rejections; got %v", got)
		}
	})

	t.Run("rejects_wrapper_struct_storing_attemptRunner", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
`,
			"wrap.go": `
package runtimehost
type runnerBag struct { runner *attemptRunner }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerOwnership(files)
		if !violationContains(got, "runnerBag") {
			t.Fatalf("expected wrapper runnerBag rejection; got %v", got)
		}
	})

	t.Run("accepts_inert_complete_shape_deps_arbitrary_name", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
`,
			"shadow.go": `
package runtimehost
type StableConfigSource interface{ ReadStable() }
type EffectiveLoader interface{ LoadEffective() }
type CandidateCompiler interface{ Compile() }
type Manager struct{}
func (m *Manager) PrepareRequestPlane() {}
func (m *Manager) Publish() {}
type ReloadObserver struct{}
type focusedWireBag struct {
	src StableConfigSource
	ld EffectiveLoader
	cls func(a, b *int) ([]int, error)
	cmp CandidateCompiler
	m *Manager
	obs *ReloadObserver
	down func() bool
}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerOwnership(files)
		if violationContains(got, "focusedWireBag") {
			t.Fatalf("inert complete-shape deps value must be accepted regardless of name; got %v", got)
		}
	})

	t.Run("rejects_complete_shape_with_multi_stage_method", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
`,
			"shadow.go": `
package runtimehost
type StableConfigSource interface{ ReadStable() }
type EffectiveLoader interface{ LoadEffective() }
type CandidateCompiler interface{ Compile() }
type Manager struct{}
func (m *Manager) PrepareRequestPlane() {}
func (m *Manager) Publish() {}
type focusedWireBag struct {
	src StableConfigSource
	ld EffectiveLoader
	cls func(a, b *int) ([]int, error)
	cmp CandidateCompiler
	m *Manager
}
func (s *focusedWireBag) execute() {
	s.src.ReadStable()
	s.ld.LoadEffective()
	s.cmp.Compile()
	s.m.PrepareRequestPlane()
}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerOwnership(files)
		if !violationContains(got, "focusedWireBag") {
			t.Fatalf("expected operational complete-shape owner rejection; got %v", got)
		}
	})

	t.Run("rejects_workflow_methods_on_attemptRunnerDeps_name", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
type StableConfigSource interface{ ReadStable() }
type EffectiveLoader interface{ LoadEffective() }
type CandidateCompiler interface{ Compile() }
type Manager struct{}
func (m *Manager) PrepareRequestPlane() {}
func (m *Manager) Publish() {}
type attemptRunnerDeps struct {
	Source StableConfigSource
	Loader EffectiveLoader
	Classify func(a, b *int) ([]int, error)
	Compile CandidateCompiler
	Manager *Manager
}
func (d *attemptRunnerDeps) Run() {
	d.Source.ReadStable()
	d.Loader.LoadEffective()
	d.Compile.Compile()
	d.Manager.Publish()
}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerOwnership(files)
		if !violationContains(got, "attemptRunnerDeps") {
			t.Fatalf("expected attemptRunnerDeps workflow-method rejection (no name exemption); got %v", got)
		}
	})

	t.Run("rejects_workflow_methods_on_CoordinatorDeps_name", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
`,
			"coordinator.go": `
package runtimehost
type StableConfigSource interface{ ReadStable() }
type EffectiveLoader interface{ LoadEffective() }
type CandidateCompiler interface{ Compile() }
type Manager struct{}
func (m *Manager) PrepareRequestPlane() {}
func (m *Manager) Publish() {}
type CoordinatorDeps struct {
	Source StableConfigSource
	Loader EffectiveLoader
	Classify func(a, b *int) ([]int, error)
	Compile CandidateCompiler
	Manager *Manager
}
func (d *CoordinatorDeps) sneak() {
	d.Source.ReadStable()
	d.Loader.LoadEffective()
}
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerOwnership(files)
		if !violationContains(got, "CoordinatorDeps") {
			t.Fatalf("expected CoordinatorDeps workflow-method rejection (no name exemption); got %v", got)
		}
	})

	t.Run("rejects_free_helper_combining_stage_roles", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
`,
			"helper.go": `
package runtimehost
type StableConfigSource interface{ ReadStable() }
type EffectiveLoader interface{ LoadEffective() }
type CandidateCompiler interface{ Compile() }
type Manager struct{}
func (m *Manager) PrepareRequestPlane() {}
func (m *Manager) Publish() {}
func sneakTxn(src StableConfigSource, ld EffectiveLoader, cmp CandidateCompiler, m *Manager) {
	src.ReadStable()
	ld.LoadEffective()
	cmp.Compile()
	m.PrepareRequestPlane()
}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerOwnership(files)
		if !violationContains(got, "sneakTxn") && !violationContains(got, "multiple") {
			t.Fatalf("expected free helper multi-stage rejection; got %v", got)
		}
	})

	t.Run("accepts_partial_dependency_and_one_role_helpers", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
`,
			"partial.go": `
package runtimehost
type StableConfigSource interface{ ReadStable() }
type EffectiveLoader interface{ LoadEffective() }
type Manager struct{}
type partialHelper struct {
	src StableConfigSource
	ld EffectiveLoader
	m *Manager
}
func onlyRead(src StableConfigSource) { src.ReadStable() }
func onlyLoad(ld EffectiveLoader) { ld.LoadEffective() }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerOwnership(files)
		if violationContains(got, "partialHelper") || violationContains(got, "onlyRead") || violationContains(got, "onlyLoad") {
			t.Fatalf("partial deps / one-role helpers must not be flagged; got %v", got)
		}
	})

	t.Run("rejects_complete_shape_Run_split_across_receiver_helpers", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
`,
			"shadow.go": `
package runtimehost
type StableConfigSource interface{ ReadStable() }
type EffectiveLoader interface{ LoadEffective() }
type CandidateCompiler interface{ Compile() }
type Manager struct{}
func (m *Manager) PrepareRequestPlane() {}
func (m *Manager) Publish() {}
type shadowTxn struct {
	source StableConfigSource
	loader EffectiveLoader
	cls func(a, b *int) ([]int, error)
	compiler CandidateCompiler
	manager *Manager
}
func (s *shadowTxn) read() { s.source.ReadStable() }
func (s *shadowTxn) load() { s.loader.LoadEffective() }
func (s *shadowTxn) Run() { s.read(); s.load() }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerOwnership(files)
		if !violationContains(got, "shadowTxn") {
			t.Fatalf("expected split-helper complete-shape Run rejection; got %v", got)
		}
	})

	t.Run("rejects_free_orchestrator_splitting_roles_across_helpers", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
`,
			"helper.go": `
package runtimehost
type StableConfigSource interface{ ReadStable() }
type EffectiveLoader interface{ LoadEffective() }
func onlyRead(src StableConfigSource) { src.ReadStable() }
func onlyLoad(ld EffectiveLoader) { ld.LoadEffective() }
func orchestrate(src StableConfigSource, ld EffectiveLoader) {
	onlyRead(src)
	onlyLoad(ld)
}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerOwnership(files)
		if !violationContains(got, "orchestrate") {
			t.Fatalf("expected free orchestrator transitive multi-role rejection; got %v", got)
		}
	})

	t.Run("rejects_two_hop_delegation_chain_multi_role", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
`,
			"chain.go": `
package runtimehost
type StableConfigSource interface{ ReadStable() }
type EffectiveLoader interface{ LoadEffective() }
func leaf(src StableConfigSource, ld EffectiveLoader) {
	src.ReadStable()
	ld.LoadEffective()
}
func middle(src StableConfigSource, ld EffectiveLoader) { leaf(src, ld) }
func entry(src StableConfigSource, ld EffectiveLoader) { middle(src, ld) }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerOwnership(files)
		if !violationContains(got, "entry") && !violationContains(got, "middle") && !violationContains(got, "leaf") {
			t.Fatalf("expected two-hop delegation multi-role rejection; got %v", got)
		}
	})

	t.Run("accepts_cycle_with_one_or_no_stage_role", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
`,
			"cycle.go": `
package runtimehost
type StableConfigSource interface{ ReadStable() }
func ping(src StableConfigSource) { pong(src) }
func pong(src StableConfigSource) { ping(src); src.ReadStable() }
func spin() { spin() }
func tick() { tock() }
func tock() { tick() }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerOwnership(files)
		if violationContains(got, "ping") || violationContains(got, "pong") ||
			violationContains(got, "spin") || violationContains(got, "tick") || violationContains(got, "tock") {
			t.Fatalf("one-role/no-role cycles must terminate analysis and remain accepted; got %v", got)
		}
	})

	t.Run("accepts_one_role_delegated_and_unrelated_call_graphs", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
`,
			"ok.go": `
package runtimehost
type StableConfigSource interface{ ReadStable() }
type EffectiveLoader interface{ LoadEffective() }
type CandidateCompiler interface{ Compile() }
type Manager struct{}
func (m *Manager) PrepareRequestPlane() {}
func (m *Manager) Publish() {}
type focusedWireBag struct {
	src StableConfigSource
	ld EffectiveLoader
	cls func(a, b *int) ([]int, error)
	cmp CandidateCompiler
	m *Manager
}
func (s *focusedWireBag) onlyRead() { s.src.ReadStable() }
func (s *focusedWireBag) callRead() { s.onlyRead() }
func onlyRead(src StableConfigSource) { src.ReadStable() }
func wrapRead(src StableConfigSource) { onlyRead(src) }
func unrelatedA() { unrelatedB() }
func unrelatedB() {}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerOwnership(files)
		if violationContains(got, "focusedWireBag") || violationContains(got, "onlyRead") ||
			violationContains(got, "callRead") || violationContains(got, "wrapRead") ||
			violationContains(got, "unrelatedA") || violationContains(got, "unrelatedB") {
			t.Fatalf("one-role delegated helpers / unrelated call graphs must be accepted; got %v", got)
		}
	})

	t.Run("unrelated_same_name_type_in_other_scan_untouched", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
`,
			"unrelated.go": `
package runtimehost
type widgetRunner struct{}
func newWidgetRunner() *widgetRunner { return &widgetRunner{} }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerOwnership(files)
		if len(got) > 0 {
			t.Fatalf("unrelated widgetRunner type must not be flagged; got %v", got)
		}
	})

	t.Run("rejects_newAttemptRunner_value_alias_and_call", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
`,
			"alias_ctor.go": `
package runtimehost
var makeRunner = newAttemptRunner
func extra() *attemptRunner { return makeRunner() }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerConstructorGraph(files)
		if !violationContains(got, "makeRunner") && !violationContains(got, "newAttemptRunner") {
			t.Fatalf("expected constructor alias/extra call rejection; got %v", got)
		}
	})

	t.Run("rejects_local_constructor_alias_in_NewCoordinator", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator {
	ctor := newAttemptRunner
	return &Coordinator{runner: ctor()}
}
`,
		})
		got := analyzeRunnerConstructorGraph(files)
		if !violationContains(got, "ctor") && !violationContains(got, "aliased") {
			t.Fatalf("expected local constructor alias rejection; got %v", got)
		}
	})

	t.Run("rejects_direct_attemptRunner_allocations_despite_ctor_decoy", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
func extra() *attemptRunner { return &attemptRunner{} }
func extra2() *attemptRunner { return new(attemptRunner) }
func extra3() *attemptRunner { var r attemptRunner; return &r }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerConstructorGraph(files)
		if !violationContains(got, "extra") || !violationContains(got, "extra2") || !violationContains(got, "extra3") {
			t.Fatalf("expected direct allocation forms rejected despite ctor decoy; got %v", got)
		}
	})

	t.Run("rejects_package_global_attemptRunner_instance", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
var leaked attemptRunner
var leakedPtr = &attemptRunner{}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerConstructorGraph(files)
		if !violationContains(got, "zero-value") && !violationContains(got, "composite allocation") && !violationContains(got, "package-scope") {
			t.Fatalf("expected package-global attemptRunner instance rejection; got %v", got)
		}
	})

	t.Run("rejects_aliased_defined_type_allocations_outside_ctor", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
type runnerAlias = attemptRunner
type runnerDefined attemptRunner
func viaAlias() *runnerAlias { return &runnerAlias{} }
func viaDefined() *runnerDefined { return new(runnerDefined) }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerConstructorGraph(files)
		if !violationContains(got, "viaAlias") || !violationContains(got, "viaDefined") {
			t.Fatalf("expected aliased/defined type allocation rejection; got %v", got)
		}
	})

	t.Run("accepts_widgetRunner_allocations_and_attemptRunner_params", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
func (r *attemptRunner) Run() {}
func useRunner(r *attemptRunner) { _ = r }
type widgetRunner struct{}
func newWidget() *widgetRunner { return &widgetRunner{} }
func newWidget2() *widgetRunner { return new(widgetRunner) }
func useWidget(w *widgetRunner) { var local widgetRunner; _ = local; _ = w }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerConstructorGraph(files)
		if len(got) > 0 {
			t.Fatalf("widgetRunner allocations and *attemptRunner params/receivers must be accepted; got %v", got)
		}
	})

	t.Run("rejects_two_allocations_inside_newAttemptRunner", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner {
	decoy := &attemptRunner{}
	extra := &attemptRunner{}
	_ = extra
	return decoy
}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerConstructorGraph(files)
		if !violationContains(got, "exactly one") && !violationContains(got, "got 2") {
			t.Fatalf("expected multiple allocations inside newAttemptRunner rejection; got %v", got)
		}
	})

	t.Run("rejects_mixed_allocation_forms_inside_newAttemptRunner", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner {
	a := &attemptRunner{}
	b := new(attemptRunner)
	var c attemptRunner
	_ = b
	_ = c
	return a
}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
`,
		})
		got := analyzeRunnerConstructorGraph(files)
		if !violationContains(got, "exactly one") {
			t.Fatalf("expected mixed allocation forms inside newAttemptRunner rejection; got %v", got)
		}
	})

	t.Run("rejects_extra_Run_caller_outside_reload", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
func (r *attemptRunner) Run() {}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
func (c *Coordinator) Reload() { c.runner.Run() }
func (c *Coordinator) sneak() { c.runner.Run() }
`,
		})
		sites, _ := findAttemptRunnerRunSites(files)
		bad := 0
		for _, s := range sites {
			if s.file != "coordinator.go" || !strings.HasSuffix(s.fn, "Coordinator.Reload") {
				bad++
			}
		}
		if bad == 0 {
			t.Fatal("expected extra Run caller outside Reload to be detected")
		}
	})

	t.Run("rejects_param_receiver_Run_helper_evasion", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
func (r *attemptRunner) Run() {}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
func (c *Coordinator) Reload() { c.runner.Run() }
`,
			"extra.go": `
package runtimehost
func extra(r *attemptRunner) { r.Run() }
`,
		})
		sites, _ := findAttemptRunnerRunSites(files)
		bad := 0
		for _, s := range sites {
			if s.file != "coordinator.go" || !strings.HasSuffix(s.fn, "Coordinator.Reload") {
				bad++
			}
		}
		if bad == 0 {
			t.Fatal("expected param-receiver Run helper evasion to be detected")
		}
	})

	t.Run("rejects_local_runner_alias_Run_call", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
func (r *attemptRunner) Run() {}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
func (c *Coordinator) Reload() { c.runner.Run() }
func (c *Coordinator) extra() {
	r := c.runner
	r.Run()
}
`,
		})
		sites, _ := findAttemptRunnerRunSites(files)
		bad := 0
		for _, s := range sites {
			if s.file != "coordinator.go" || !strings.HasSuffix(s.fn, "Coordinator.Reload") {
				bad++
			}
		}
		if bad == 0 {
			t.Fatal("expected local runner alias Run call to be detected")
		}
	})

	t.Run("rejects_method_value_alias_of_Run", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
func (r *attemptRunner) Run() {}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
func (c *Coordinator) Reload() {
	run := c.runner.Run
	run()
}
`,
		})
		_, methodVals := findAttemptRunnerRunSites(files)
		if !violationContains(methodVals, "method-value") {
			t.Fatalf("expected method-value Run alias rejection; got %v", methodVals)
		}
	})

	t.Run("unrelated_Run_method_on_other_type_not_counted", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
func (r *attemptRunner) Run() {}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
func (c *Coordinator) Reload() { c.runner.Run() }
`,
			"decoy.go": `
package runtimehost
type decoyRunner struct{}
func (d *decoyRunner) Run() {}
func useDecoy(d *decoyRunner) { d.Run() }
`,
		})
		sites, _ := findAttemptRunnerRunSites(files)
		for _, s := range sites {
			if s.file == "decoy.go" {
				t.Fatalf("unrelated decoyRunner.Run call must not be counted as attemptRunner.Run: %+v", s)
			}
		}
	})

	t.Run("unrelated_runner_field_not_counted", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func newAttemptRunner() *attemptRunner { return &attemptRunner{} }
func (r *attemptRunner) Run() {}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { runner *attemptRunner }
func NewCoordinator() *Coordinator { return &Coordinator{runner: newAttemptRunner()} }
func (c *Coordinator) Reload() { c.runner.Run() }
`,
			"decoy.go": `
package runtimehost
type widget struct{ runner *decoyRunner }
type decoyRunner struct{}
func (d *decoyRunner) Run() {}
func (w *widget) kick() { w.runner.Run() }
`,
		})
		sites, _ := findAttemptRunnerRunSites(files)
		for _, s := range sites {
			if s.file == "decoy.go" {
				t.Fatalf("unrelated widget.runner.Run must not be counted: %+v", s)
			}
		}
	})

	t.Run("rejects_coordinator_calling_source_read_stable_directly", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"coordinator.go": `
package runtimehost
type StableConfigSource interface{ ReadStable(); AbsolutePath() string }
type Coordinator struct { source StableConfigSource }
func (c *Coordinator) Reload() {
	c.source.ReadStable()
}
`,
		})
		got := forbiddenCoordinatorReloadExecutionCalls(files)
		if !violationContains(got, "ReadStable") {
			t.Fatalf("expected ReadStable direct-call rejection; got %v", got)
		}
	})

	t.Run("rejects_coordinator_local_alias_read_stable", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"coordinator.go": `
package runtimehost
type StableConfigSource interface{ ReadStable(); AbsolutePath() string }
type Coordinator struct { source StableConfigSource }
func (c *Coordinator) Reload() {
	src := c.source
	src.ReadStable()
}
`,
		})
		got := forbiddenCoordinatorReloadExecutionCalls(files)
		if !violationContains(got, "ReadStable") {
			t.Fatalf("expected aliased ReadStable rejection; got %v", got)
		}
	})

	t.Run("rejects_coordinator_wrapper_receiving_loader", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"coordinator.go": `
package runtimehost
type EffectiveLoader interface{ LoadEffective() }
type Coordinator struct{}
func (c *Coordinator) helper(ld EffectiveLoader) { ld.LoadEffective() }
`,
		})
		got := forbiddenCoordinatorReloadExecutionCalls(files)
		if !violationContains(got, "LoadEffective") {
			t.Fatalf("expected wrapper LoadEffective rejection; got %v", got)
		}
	})

	t.Run("rejects_coordinator_aliased_classify_call", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"coordinator.go": `
package runtimehost
type Coordinator struct {
	classify func(a, b int) error
}
func (c *Coordinator) Reload() {
	fn := c.classify
	_ = fn(1, 2)
}
`,
		})
		got := forbiddenCoordinatorReloadExecutionCalls(files)
		if !violationContains(got, "classify") {
			t.Fatalf("expected aliased classify rejection; got %v", got)
		}
	})

	t.Run("rejects_coordinator_calling_compile_via_alias", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"coordinator.go": `
package runtimehost
type CandidateCompiler interface{ Compile() }
type Coordinator struct { compile CandidateCompiler }
func (c *Coordinator) Reload() {
	cmp := c.compile
	cmp.Compile()
}
`,
		})
		got := forbiddenCoordinatorReloadExecutionCalls(files)
		if !violationContains(got, "Compile") {
			t.Fatalf("expected aliased Compile rejection; got %v", got)
		}
	})

	t.Run("accepts_coordinator_fixed_source_path_only", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"coordinator.go": `
package runtimehost
type StableConfigSource interface{ ReadStable(); AbsolutePath() string }
type Coordinator struct { source StableConfigSource }
`,
			"coordinator_fixed_source.go": `
package runtimehost
func (c *Coordinator) FixedSourcePath() string {
	if c == nil || c.source == nil { return "" }
	return c.source.AbsolutePath()
}
`,
		})
		got := forbiddenCoordinatorReloadExecutionCalls(files)
		if len(got) > 0 {
			t.Fatalf("AbsolutePath-only source access must remain accepted; got %v", got)
		}
	})

	t.Run("rejects_coordinator_calling_manager_publish_directly", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"coordinator.go": `
package runtimehost
type Manager struct{}
func (m *Manager) Publish() {}
func (m *Manager) PrepareRequestPlane() {}
func (m *Manager) Active() {}
type Coordinator struct { mgr *Manager }
func (c *Coordinator) Reload() {
	c.mgr.Publish()
}
`,
		})
		got := forbiddenCoordinatorReloadExecutionCalls(files)
		if !violationContains(got, "Publish") {
			t.Fatalf("expected Publish direct-call rejection; got %v", got)
		}
	})

	t.Run("accepts_coordinator_calling_unrelated_manager_methods", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"coordinator.go": `
package runtimehost
type Manager struct{}
func (m *Manager) Publish() {}
func (m *Manager) PrepareRequestPlane() {}
func (m *Manager) Active() {}
func (m *Manager) ObservabilitySnapshot() {}
func (m *Manager) ShuttingDown() bool { return false }
func (m *Manager) BeginShutdown() {}
type Coordinator struct { mgr *Manager }
func (c *Coordinator) Status() {
	c.mgr.Active()
	c.mgr.ObservabilitySnapshot()
	c.mgr.ShuttingDown()
	c.mgr.BeginShutdown()
}
`,
		})
		got := forbiddenCoordinatorReloadExecutionCalls(files)
		if len(got) > 0 {
			t.Fatalf("unrelated Manager status/shutdown methods must remain accepted; got %v", got)
		}
	})

	t.Run("accepts_unrelated_same_name_methods_on_other_types", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"coordinator.go": `
package runtimehost
type otherSrc struct{}
func (o *otherSrc) ReadStable() {}
type Coordinator struct{}
func (c *Coordinator) helper(o *otherSrc) { o.ReadStable() }
`,
		})
		got := forbiddenCoordinatorReloadExecutionCalls(files)
		if len(got) > 0 {
			t.Fatalf("unrelated same-name methods must remain accepted; got %v", got)
		}
	})

	t.Run("rejects_runner_referencing_attemptGate_type", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct {
	gate *attemptGate
}
`,
		})
		got := scanRunnerFileForForbiddenOwnership(files["attempt_runner.go"])
		if !violationContains(got, "attemptGate") {
			t.Fatalf("expected attemptGate field rejection; got %v", got)
		}
	})

	t.Run("rejects_runner_calling_gate_transition_method", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func (r *attemptRunner) bad(g *attemptGate) { g.TryStart() }
`,
		})
		got := scanRunnerFileForForbiddenOwnership(files["attempt_runner.go"])
		if !violationContains(got, "TryStart") {
			t.Fatalf("expected gate transition method call rejection; got %v", got)
		}
	})

	t.Run("rejects_runner_calling_recordTerminal", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct{}
func (r *attemptRunner) bad(c *Coordinator) { c.recordTerminal() }
`,
		})
		got := scanRunnerFileForForbiddenOwnership(files["attempt_runner.go"])
		if !violationContains(got, "recordTerminal") {
			t.Fatalf("expected recordTerminal call rejection; got %v", got)
		}
	})

	t.Run("accepts_runner_with_narrow_shutdown_predicate_field", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_runner.go": `
package runtimehost
type attemptRunner struct {
	shuttingDown func() bool
}
`,
		})
		got := scanRunnerFileForForbiddenOwnership(files["attempt_runner.go"])
		if len(got) > 0 {
			t.Fatalf("opaque shutdown predicate field must remain accepted; got %v", got)
		}
	})
}

// analyzeRunnerOwnership fails closed on duplicate/aliased attemptRunner
// ownership, non-Coordinator storage, and operational (behavior-proven)
// equivalent transaction owners. Complete collaborator field shape alone is
// not ownership — inert deps bags remain accepted regardless of type name.
func analyzeRunnerOwnership(files map[string]*ast.File) []string {
	var violations []string
	aliases := collectPackageTypeAliases(files)

	var runnerFiles []string
	for path, file := range files {
		ts := findTypeSpec(file, "attemptRunner")
		if ts == nil {
			continue
		}
		if _, ok := ts.Type.(*ast.StructType); ok && ts.Assign == 0 {
			runnerFiles = append(runnerFiles, path)
		}
	}
	if len(runnerFiles) != 1 || runnerFiles[0] != "attempt_runner.go" {
		violations = append(violations, "want exactly one attemptRunner struct declaration in attempt_runner.go; got "+strings.Join(runnerFiles, ","))
	}

	for path, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil || ts.Name.Name == "attemptRunner" {
					continue
				}
				under := resolveTypeString(ts.Type, aliases)
				if under == "attemptRunner" || under == "*attemptRunner" {
					kind := "defined type"
					if ts.Assign != 0 {
						kind = "alias"
					}
					violations = append(violations, path+": "+kind+" "+ts.Name.Name+" of attemptRunner/*attemptRunner")
				}
			}
		}
	}

	for path, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				if ts.Name.Name == "Coordinator" {
					runnerFields := 0
					for _, f := range st.Fields.List {
						if !storesAttemptRunner(f.Type, aliases) {
							continue
						}
						for _, n := range f.Names {
							if n.Name != "runner" {
								violations = append(violations, fmt.Sprintf("%s: Coordinator runner field must be named runner; got %q", path, n.Name))
							} else {
								runnerFields++
							}
						}
						if len(f.Names) == 0 {
							violations = append(violations, fmt.Sprintf("%s: Coordinator must not embed attemptRunner", path))
						}
					}
					if runnerFields != 1 {
						violations = append(violations, fmt.Sprintf("%s: Coordinator must have exactly one runner *attemptRunner field; got %d", path, runnerFields))
					}
				} else {
					for _, f := range st.Fields.List {
						if storesAttemptRunner(f.Type, aliases) {
							name := ts.Name.Name
							if len(f.Names) > 0 {
								name = ts.Name.Name + "." + f.Names[0].Name
							}
							violations = append(violations, fmt.Sprintf("%s: non-Coordinator struct %q stores/embeds attemptRunner", path, name))
						}
					}
				}
			}
		}
	}

	violations = append(violations, analyzeOperationalTransactionOwners(files)...)
	return violations
}

func storesAttemptRunner(expr ast.Expr, aliases map[string]string) bool {
	under := resolveTypeString(expr, aliases)
	return under == "attemptRunner" || under == "*attemptRunner"
}

// hasCompleteCollaboratorShape reports whether a struct carries the full
// resolved reload-stage collaborator set (source+loader+classifier+compiler+
// manager). Shape alone does not imply transaction ownership.
func hasCompleteCollaboratorShape(st *ast.StructType, aliases map[string]string) bool {
	hasSource := false
	hasLoader := false
	hasCompile := false
	hasClassify := false
	hasManager := false
	for _, f := range st.Fields.List {
		typ := resolveTypeString(f.Type, aliases)
		switch {
		case typ == "StableConfigSource":
			hasSource = true
		case typ == "EffectiveLoader":
			hasLoader = true
		case typ == "CandidateCompiler":
			hasCompile = true
		case typ == "*Manager" || typ == "Manager":
			hasManager = true
		case isClassifierFuncType(f.Type):
			hasClassify = true
		}
	}
	return hasSource && hasLoader && hasCompile && hasClassify && hasManager
}

func isClassifierFuncType(expr ast.Expr) bool {
	ft, ok := unwrapParen(expr).(*ast.FuncType)
	if !ok || ft.Params == nil {
		return false
	}
	n := 0
	for _, f := range ft.Params.List {
		if len(f.Names) == 0 {
			n++
		} else {
			n += len(f.Names)
		}
	}
	return n >= 2
}

// analyzeOperationalTransactionOwners rejects noncanonical types/functions
// that execute the detailed attempt workflow, including workflows split across
// package-local helpers. Inert complete-shape deps values are accepted; exact
// type-name exemptions are not used. Canonical attemptRunner methods are the
// sole allowed composed workflow owner.
func analyzeOperationalTransactionOwners(files map[string]*ast.File) []string {
	var violations []string
	aliases := collectPackageTypeAliases(files)
	fieldProv := collectStructFieldStageProv(files, aliases)

	completeShapes := map[string]bool{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil || ts.Name.Name == "attemptRunner" {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				if hasCompleteCollaboratorShape(st, aliases) {
					completeShapes[ts.Name.Name] = true
				}
			}
		}
	}

	infos := collectPackageStageCallGraph(files, aliases, fieldProv)
	transitive := computeTransitiveStageRoles(infos)

	for id, info := range infos {
		if info.recvType == "attemptRunner" {
			continue
		}
		roles := transitive[id]
		if len(roles) >= 2 {
			if info.recvType != "" && completeShapes[info.recvType] {
				violations = append(violations, fmt.Sprintf("%s: operational transaction owner %q method %s invokes multiple workflow stage roles", info.path, info.recvType, info.display))
			} else {
				violations = append(violations, fmt.Sprintf("%s: noncanonical function %s combines multiple detailed stage-role calls", info.path, info.display))
			}
			continue
		}
		if info.recvType != "" && completeShapes[info.recvType] && info.name == "Run" && len(roles) >= 1 {
			violations = append(violations, fmt.Sprintf("%s: complete-shape type %q exposes Runner-like entry point %s backed by stage roles", info.path, info.recvType, info.display))
		}
	}
	return violations
}

type packageFnID string

type packageFnInfo struct {
	path     string
	display  string
	recvType string
	name     string
	direct   map[stageValueProv]bool
	callees  []packageFnID
}

// collectPackageStageCallGraph assigns stable identities to package
// functions/methods, records direct stage-role sets, and records direct
// package-local call/delegation edges (including straightforward local
// receiver and callable aliases).
func collectPackageStageCallGraph(
	files map[string]*ast.File,
	aliases map[string]string,
	fieldProv map[string]map[string]stageValueProv,
) map[packageFnID]*packageFnInfo {
	infos := map[packageFnID]*packageFnInfo{}
	freeFuncs := map[string]packageFnID{}
	methods := map[string]map[string]packageFnID{} // type -> method -> id

	for path, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Body == nil {
				continue
			}
			id, recvType := packageFnIdentity(fd, aliases)
			infos[id] = &packageFnInfo{
				path:     path,
				display:  funcDisplayName(fd),
				recvType: recvType,
				name:     fd.Name.Name,
				direct:   map[stageValueProv]bool{},
			}
			if recvType == "" {
				freeFuncs[fd.Name.Name] = id
			} else {
				if methods[recvType] == nil {
					methods[recvType] = map[string]packageFnID{}
				}
				methods[recvType][fd.Name.Name] = id
			}
		}
	}

	for path, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Body == nil {
				continue
			}
			id, _ := packageFnIdentity(fd, aliases)
			info := infos[id]
			recvTypes := map[string]string{}
			env := map[string]stageValueProv{}
			funcAlias := map[string]packageFnID{}
			methodAlias := map[string]packageFnID{}

			if fd.Recv != nil {
				for _, f := range fd.Recv.List {
					typ := strings.TrimPrefix(resolveTypeString(f.Type, aliases), "*")
					for _, n := range f.Names {
						if n != nil {
							recvTypes[n.Name] = typ
						}
					}
				}
			}
			if fd.Type != nil && fd.Type.Params != nil {
				for _, f := range fd.Type.Params.List {
					p := stageProvFromTypeExpr(f.Type, aliases)
					if p != stageProvUnknown {
						for _, n := range f.Names {
							if n != nil {
								env[n.Name] = p
							}
						}
					}
					under := strings.TrimPrefix(resolveTypeString(f.Type, aliases), "*")
					if under != "" {
						for _, n := range f.Names {
							if n != nil {
								recvTypes[n.Name] = under
							}
						}
					}
				}
			}
			info.direct = collectInvokedStageRoles(fd.Body, cloneStageProv(env), fieldProv, cloneStringMap(recvTypes))
			info.callees = collectPackageCallees(fd.Body, freeFuncs, methods, recvTypes, funcAlias, methodAlias, aliases)
			_ = path
		}
	}
	return infos
}

func packageFnIdentity(fd *ast.FuncDecl, aliases map[string]string) (packageFnID, string) {
	name := ""
	if fd.Name != nil {
		name = fd.Name.Name
	}
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return packageFnID(name), ""
	}
	recvType := strings.TrimPrefix(resolveTypeString(fd.Recv.List[0].Type, aliases), "*")
	return packageFnID(recvType + "." + name), recvType
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

// collectPackageCallees records direct calls/delegations to package functions
// and same-package methods, tracking local aliases of callables and receivers.
func collectPackageCallees(
	body *ast.BlockStmt,
	freeFuncs map[string]packageFnID,
	methods map[string]map[string]packageFnID,
	recvTypes map[string]string,
	funcAlias map[string]packageFnID,
	methodAlias map[string]packageFnID,
	aliases map[string]string,
) []packageFnID {
	seen := map[packageFnID]bool{}
	var callees []packageFnID
	note := func(id packageFnID) {
		if id == "" || seen[id] {
			return
		}
		// Delegation into the canonical owner is not stage-role composition.
		if strings.HasPrefix(string(id), "attemptRunner.") {
			return
		}
		seen[id] = true
		callees = append(callees, id)
	}
	resolveCallableIdent := func(name string) packageFnID {
		if id, ok := funcAlias[name]; ok {
			return id
		}
		if id, ok := methodAlias[name]; ok {
			return id
		}
		if id, ok := freeFuncs[name]; ok {
			return id
		}
		return ""
	}
	resolveMethod := func(recvTyp, method string) packageFnID {
		if recvTyp == "" || method == "" {
			return ""
		}
		if m := methods[recvTyp]; m != nil {
			return m[method]
		}
		return ""
	}
	var bindAliasFromExpr func(name string, expr ast.Expr)
	bindAliasFromExpr = func(name string, expr ast.Expr) {
		if name == "" || name == "_" || expr == nil {
			return
		}
		switch e := unwrapParen(expr).(type) {
		case *ast.Ident:
			if id := resolveCallableIdent(e.Name); id != "" {
				if strings.Contains(string(id), ".") {
					methodAlias[name] = id
				} else {
					funcAlias[name] = id
				}
			}
			if typ, ok := recvTypes[e.Name]; ok {
				recvTypes[name] = typ
			}
		case *ast.SelectorExpr:
			if e.Sel == nil {
				return
			}
			recv, ok := e.X.(*ast.Ident)
			if !ok {
				return
			}
			if typ, ok := recvTypes[recv.Name]; ok {
				if id := resolveMethod(typ, e.Sel.Name); id != "" {
					methodAlias[name] = id
				}
			}
		case *ast.UnaryExpr:
			if e.Op == token.AND {
				bindAliasFromExpr(name, e.X)
			}
		case *ast.StarExpr:
			bindAliasFromExpr(name, e.X)
		}
	}
	noteCall := func(call *ast.CallExpr) {
		switch fun := unwrapParen(call.Fun).(type) {
		case *ast.Ident:
			note(resolveCallableIdent(fun.Name))
		case *ast.SelectorExpr:
			if fun.Sel == nil {
				return
			}
			recv, ok := fun.X.(*ast.Ident)
			if !ok {
				return
			}
			if typ, ok := recvTypes[recv.Name]; ok {
				note(resolveMethod(typ, fun.Sel.Name))
			}
		}
	}

	var walk func(body *ast.BlockStmt)
	walk = func(body *ast.BlockStmt) {
		if body == nil {
			return
		}
		for _, stmt := range body.List {
			switch s := stmt.(type) {
			case *ast.AssignStmt:
				for _, rhs := range s.Rhs {
					ast.Inspect(rhs, func(n ast.Node) bool {
						if call, ok := n.(*ast.CallExpr); ok {
							noteCall(call)
						}
						return true
					})
				}
				for i, rhs := range s.Rhs {
					if i >= len(s.Lhs) {
						continue
					}
					lhs, ok := s.Lhs[i].(*ast.Ident)
					if !ok {
						continue
					}
					bindAliasFromExpr(lhs.Name, rhs)
				}
			case *ast.DeclStmt:
				gd, ok := s.Decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					if vs.Type != nil {
						under := strings.TrimPrefix(resolveTypeString(vs.Type, aliases), "*")
						if under != "" {
							for _, name := range vs.Names {
								if name != nil {
									recvTypes[name.Name] = under
								}
							}
						}
					}
					for i, name := range vs.Names {
						if name == nil || i >= len(vs.Values) {
							continue
						}
						ast.Inspect(vs.Values[i], func(n ast.Node) bool {
							if call, ok := n.(*ast.CallExpr); ok {
								noteCall(call)
							}
							return true
						})
						bindAliasFromExpr(name.Name, vs.Values[i])
					}
				}
			case *ast.ExprStmt:
				ast.Inspect(s.X, func(n ast.Node) bool {
					if call, ok := n.(*ast.CallExpr); ok {
						noteCall(call)
					}
					return true
				})
			case *ast.ReturnStmt:
				for _, r := range s.Results {
					ast.Inspect(r, func(n ast.Node) bool {
						if call, ok := n.(*ast.CallExpr); ok {
							noteCall(call)
						}
						return true
					})
				}
			case *ast.DeferStmt:
				if s.Call == nil {
					continue
				}
				if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
					walk(lit.Body)
					continue
				}
				noteCall(s.Call)
			case *ast.GoStmt:
				if s.Call == nil {
					continue
				}
				if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
					walk(lit.Body)
					continue
				}
				noteCall(s.Call)
			case *ast.IfStmt:
				if s.Init != nil {
					if as, ok := s.Init.(*ast.AssignStmt); ok {
						for _, rhs := range as.Rhs {
							ast.Inspect(rhs, func(n ast.Node) bool {
								if call, ok := n.(*ast.CallExpr); ok {
									noteCall(call)
								}
								return true
							})
						}
						for i, rhs := range as.Rhs {
							if i >= len(as.Lhs) {
								continue
							}
							if lhs, ok := as.Lhs[i].(*ast.Ident); ok {
								bindAliasFromExpr(lhs.Name, rhs)
							}
						}
					}
				}
				walk(s.Body)
				if s.Else != nil {
					if b, ok := s.Else.(*ast.BlockStmt); ok {
						walk(b)
					} else if elif, ok := s.Else.(*ast.IfStmt); ok {
						walk(&ast.BlockStmt{List: []ast.Stmt{elif}})
					}
				}
			case *ast.BlockStmt:
				walk(s)
			case *ast.ForStmt:
				walk(s.Body)
			case *ast.RangeStmt:
				walk(s.Body)
			default:
				ast.Inspect(s, func(n ast.Node) bool {
					if call, ok := n.(*ast.CallExpr); ok {
						noteCall(call)
					}
					return true
				})
			}
		}
	}
	walk(body)
	return callees
}

// computeTransitiveStageRoles expands direct roles over the package-local call
// graph to a fixed point (cycle-safe).
func computeTransitiveStageRoles(infos map[packageFnID]*packageFnInfo) map[packageFnID]map[stageValueProv]bool {
	out := map[packageFnID]map[stageValueProv]bool{}
	for id, info := range infos {
		roles := map[stageValueProv]bool{}
		for r := range info.direct {
			roles[r] = true
		}
		out[id] = roles
	}
	for {
		changed := false
		for id, info := range infos {
			cur := out[id]
			for _, callee := range info.callees {
				for r := range out[callee] {
					if !cur[r] {
						cur[r] = true
						changed = true
					}
				}
			}
		}
		if !changed {
			break
		}
	}
	return out
}

// collectStructFieldStageProv maps struct type -> field name -> stage role
// provenance using resolved collaborator types (not lexical field names).
func collectStructFieldStageProv(files map[string]*ast.File, aliases map[string]string) map[string]map[string]stageValueProv {
	out := map[string]map[string]stageValueProv{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				fields := map[string]stageValueProv{}
				for _, f := range st.Fields.List {
					p := stageProvFromTypeExpr(f.Type, aliases)
					if p == stageProvUnknown {
						continue
					}
					for _, n := range f.Names {
						if n != nil {
							fields[n.Name] = p
						}
					}
				}
				if len(fields) > 0 {
					out[ts.Name.Name] = fields
				}
			}
		}
	}
	return out
}

// analyzeRunnerConstructorGraph requires exactly one direct newAttemptRunner
// call in NewCoordinator, rejects package/local/chained aliases and wrappers,
// and rejects any concrete attemptRunner allocation outside newAttemptRunner.
func analyzeRunnerConstructorGraph(files map[string]*ast.File) []string {
	var violations []string
	ctorCallables := collectNamedConstructorCallables(files, "newAttemptRunner")

	for path, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if name == nil || name.Name == "newAttemptRunner" || i >= len(vs.Values) {
						continue
					}
					if refersToConstructorCallable(vs.Values[i], ctorCallables) {
						violations = append(violations, fmt.Sprintf("%s: package-scope constructor alias %q of newAttemptRunner", path, name.Name))
					}
				}
			}
		}
	}

	var calls []ctorCallRecord
	for path, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fnName := ""
			if fd.Name != nil {
				fnName = fd.Name.Name
			}
			localCallables := cloneStringSet(ctorCallables)
			var walk func(body *ast.BlockStmt)
			walk = func(body *ast.BlockStmt) {
				if body == nil {
					return
				}
				for _, stmt := range body.List {
					switch s := stmt.(type) {
					case *ast.AssignStmt:
						for i, rhs := range s.Rhs {
							if refersToConstructorCallable(rhs, localCallables) {
								if i < len(s.Lhs) {
									if id, ok := s.Lhs[i].(*ast.Ident); ok && id.Name != "_" && id.Name != "newAttemptRunner" {
										localCallables[id.Name] = true
										violations = append(violations, fmt.Sprintf("%s: local constructor alias %q of newAttemptRunner in %s", path, id.Name, fnName))
									}
								}
							}
							inspectNamedConstructorCalls(rhs, path, fnName, "newAttemptRunner", localCallables, &calls)
						}
					case *ast.DeclStmt:
						gd, ok := s.Decl.(*ast.GenDecl)
						if !ok || gd.Tok != token.VAR {
							continue
						}
						for _, spec := range gd.Specs {
							vs, ok := spec.(*ast.ValueSpec)
							if !ok {
								continue
							}
							for i, name := range vs.Names {
								if name == nil {
									continue
								}
								if i < len(vs.Values) && refersToConstructorCallable(vs.Values[i], localCallables) {
									if name.Name != "newAttemptRunner" {
										localCallables[name.Name] = true
										violations = append(violations, fmt.Sprintf("%s: local constructor alias %q of newAttemptRunner in %s", path, name.Name, fnName))
									}
								}
								if i < len(vs.Values) {
									inspectNamedConstructorCalls(vs.Values[i], path, fnName, "newAttemptRunner", localCallables, &calls)
								}
							}
						}
					case *ast.DeferStmt:
						if s.Call == nil {
							continue
						}
						if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
							walk(lit.Body)
							continue
						}
						inspectNamedConstructorCalls(s.Call, path, fnName, "newAttemptRunner", localCallables, &calls)
					case *ast.GoStmt:
						if s.Call == nil {
							continue
						}
						if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
							walk(lit.Body)
							continue
						}
						inspectNamedConstructorCalls(s.Call, path, fnName, "newAttemptRunner", localCallables, &calls)
					case *ast.IfStmt:
						if s.Init != nil {
							if as, ok := s.Init.(*ast.AssignStmt); ok {
								for i, rhs := range as.Rhs {
									if refersToConstructorCallable(rhs, localCallables) {
										if i < len(as.Lhs) {
											if id, ok := as.Lhs[i].(*ast.Ident); ok && id.Name != "_" && id.Name != "newAttemptRunner" {
												localCallables[id.Name] = true
												violations = append(violations, fmt.Sprintf("%s: local constructor alias %q of newAttemptRunner in %s", path, id.Name, fnName))
											}
										}
									}
									inspectNamedConstructorCalls(rhs, path, fnName, "newAttemptRunner", localCallables, &calls)
								}
							}
						}
						walk(s.Body)
						if s.Else != nil {
							if b, ok := s.Else.(*ast.BlockStmt); ok {
								walk(b)
							} else if elif, ok := s.Else.(*ast.IfStmt); ok {
								walk(&ast.BlockStmt{List: []ast.Stmt{elif}})
							}
						}
					case *ast.BlockStmt:
						walk(s)
					case *ast.ForStmt:
						walk(s.Body)
					case *ast.RangeStmt:
						walk(s.Body)
					case *ast.ExprStmt:
						inspectNamedConstructorCalls(s.X, path, fnName, "newAttemptRunner", localCallables, &calls)
					case *ast.ReturnStmt:
						for _, r := range s.Results {
							inspectNamedConstructorCalls(r, path, fnName, "newAttemptRunner", localCallables, &calls)
						}
					default:
						ast.Inspect(s, func(n ast.Node) bool {
							call, ok := n.(*ast.CallExpr)
							if !ok {
								return true
							}
							recordNamedConstructorCall(call, path, fnName, "newAttemptRunner", localCallables, &calls)
							return true
						})
					}
				}
			}
			walk(fd.Body)
		}
	}

	allowed := 0
	for _, c := range calls {
		if c.direct && c.file == "coordinator.go" && c.fn == "NewCoordinator" {
			allowed++
			continue
		}
		if c.direct {
			violations = append(violations, fmt.Sprintf("%s: extra newAttemptRunner/aliased constructor call in %s", c.file, c.fn))
			continue
		}
		violations = append(violations, fmt.Sprintf("%s: aliased constructor call %q in %s", c.file, c.name, c.fn))
	}
	if allowed != 1 {
		violations = append(violations, fmt.Sprintf("want exactly one newAttemptRunner call in NewCoordinator; got %d allowed-site call(s)", allowed))
	}
	violations = append(violations, analyzeAttemptRunnerAllocations(files)...)
	return violations
}

// collectAttemptRunnerConcreteNames returns type names that resolve to the
// concrete attemptRunner (including aliases and defined rename types).
func collectAttemptRunnerConcreteNames(files map[string]*ast.File) map[string]bool {
	names := map[string]bool{"attemptRunner": true}
	aliases := collectPackageTypeAliases(files)
	for name, under := range aliases {
		u := strings.TrimPrefix(under, "*")
		if u == "attemptRunner" {
			names[name] = true
		}
	}
	return names
}

func typeExprIsAttemptRunnerConcrete(expr ast.Expr, names map[string]bool) bool {
	switch e := unwrapParen(expr).(type) {
	case *ast.Ident:
		return names[e.Name]
	case *ast.StarExpr:
		return typeExprIsAttemptRunnerConcrete(e.X, names)
	default:
		return false
	}
}

func typeExprIsConcreteAttemptRunnerValue(expr ast.Expr, names map[string]bool) bool {
	id, ok := unwrapParen(expr).(*ast.Ident)
	return ok && names[id.Name]
}

// analyzeAttemptRunnerAllocations requires exactly one concrete attemptRunner
// allocation site across the scanned files. That sole site must be inside
// newAttemptRunner in attempt_runner.go. Recognized forms: T{}, &T{}, new(T),
// concrete zero-value variable declarations, and resolved aliases/defined
// types. Parameters and receivers typed *attemptRunner are usages, not
// construction.
func analyzeAttemptRunnerAllocations(files map[string]*ast.File) []string {
	var violations []string
	names := collectAttemptRunnerConcreteNames(files)

	type allocSite struct {
		path string
		fn   string
		kind string
	}
	var sites []allocSite
	record := func(path, fn, kind string) {
		sites = append(sites, allocSite{path: path, fn: fn, kind: kind})
	}

	inspectAllocExpr := func(expr ast.Expr, path, fn string) {
		if expr == nil {
			return
		}
		ast.Inspect(expr, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.UnaryExpr:
				if x.Op != token.AND {
					return true
				}
				if lit, ok := unwrapParen(x.X).(*ast.CompositeLit); ok && lit.Type != nil && typeExprIsConcreteAttemptRunnerValue(lit.Type, names) {
					record(path, fn, "composite allocation &T{}")
					return false
				}
			case *ast.CompositeLit:
				if x.Type != nil && typeExprIsConcreteAttemptRunnerValue(x.Type, names) {
					record(path, fn, "composite allocation T{}")
					return false
				}
			case *ast.CallExpr:
				id, ok := unwrapParen(x.Fun).(*ast.Ident)
				if !ok || id.Name != "new" || len(x.Args) != 1 {
					return true
				}
				if typeExprIsConcreteAttemptRunnerValue(x.Args[0], names) {
					record(path, fn, "new(T) allocation")
					return false
				}
			}
			return true
		})
	}

	inspectValueSpec := func(vs *ast.ValueSpec, path, fn string, packageScope bool) {
		if vs.Type != nil {
			if typeExprIsConcreteAttemptRunnerValue(vs.Type, names) {
				record(path, fn, "zero-value concrete variable")
			} else if packageScope && typeExprIsAttemptRunnerConcrete(vs.Type, names) {
				record(path, fn, "package-scope instance variable")
			}
		}
		for _, v := range vs.Values {
			inspectAllocExpr(v, path, fn)
		}
	}

	for path, file := range files {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok != token.VAR {
					continue
				}
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					inspectValueSpec(vs, path, "", true)
				}
			case *ast.FuncDecl:
				if d.Body == nil {
					continue
				}
				fnName := ""
				if d.Name != nil {
					fnName = d.Name.Name
				}
				ast.Inspect(d.Body, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.ValueSpec:
						inspectValueSpec(x, path, fnName, false)
						return false
					case *ast.AssignStmt:
						for _, rhs := range x.Rhs {
							inspectAllocExpr(rhs, path, fnName)
						}
						return false
					case *ast.ReturnStmt:
						for _, r := range x.Results {
							inspectAllocExpr(r, path, fnName)
						}
						return false
					case *ast.CallExpr:
						inspectAllocExpr(x, path, fnName)
						return false
					case *ast.UnaryExpr:
						inspectAllocExpr(x, path, fnName)
						return false
					case *ast.CompositeLit:
						inspectAllocExpr(x, path, fnName)
						return false
					}
					return true
				})
			}
		}
	}

	if len(sites) != 1 {
		violations = append(violations, fmt.Sprintf("want exactly one concrete attemptRunner allocation site total; got %d", len(sites)))
	}
	for _, s := range sites {
		if s.path == "attempt_runner.go" && s.fn == "newAttemptRunner" {
			continue
		}
		if s.fn == "" {
			violations = append(violations, fmt.Sprintf("%s: %s of attemptRunner outside newAttemptRunner", s.path, s.kind))
			continue
		}
		violations = append(violations, fmt.Sprintf("%s: %s of attemptRunner in %s (only newAttemptRunner may allocate)", s.path, s.kind, s.fn))
	}
	if len(sites) == 1 {
		s := sites[0]
		if s.path != "attempt_runner.go" || s.fn != "newAttemptRunner" {
			violations = append(violations, fmt.Sprintf("%s: sole attemptRunner allocation must be inside newAttemptRunner in attempt_runner.go; found in %s", s.path, s.fn))
		}
	} else if len(sites) > 1 {
		canonical := 0
		for _, s := range sites {
			if s.path == "attempt_runner.go" && s.fn == "newAttemptRunner" {
				canonical++
			}
		}
		if canonical > 1 {
			violations = append(violations, fmt.Sprintf("attempt_runner.go: newAttemptRunner must contain exactly one concrete attemptRunner allocation; got %d", canonical))
		}
	}
	return violations
}

func collectNamedConstructorCallables(files map[string]*ast.File, canonical string) map[string]bool {
	callables := map[string]bool{canonical: true}
	for range 8 {
		changed := false
		for _, file := range files {
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range vs.Names {
						if name == nil || i >= len(vs.Values) {
							continue
						}
						if refersToConstructorCallable(vs.Values[i], callables) && !callables[name.Name] {
							callables[name.Name] = true
							changed = true
						}
					}
				}
			}
		}
		if !changed {
			break
		}
	}
	return callables
}

func inspectNamedConstructorCalls(expr ast.Expr, path, fn, canonical string, callables map[string]bool, calls *[]ctorCallRecord) {
	ast.Inspect(expr, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		recordNamedConstructorCall(call, path, fn, canonical, callables, calls)
		return true
	})
}

func recordNamedConstructorCall(call *ast.CallExpr, path, fn, canonical string, callables map[string]bool, calls *[]ctorCallRecord) {
	id, ok := unwrapParen(call.Fun).(*ast.Ident)
	if !ok || !callables[id.Name] {
		return
	}
	*calls = append(*calls, ctorCallRecord{
		file:   path,
		fn:     fn,
		direct: id.Name == canonical,
		name:   id.Name,
	})
}

type runCallSite struct {
	file string
	fn   string
}

type runnerValueProv int

const (
	runnerProvUnknown runnerValueProv = iota
	runnerProvAttemptRunner
)

// findAttemptRunnerRunSites proves Run receivers through typed *attemptRunner
// params/receivers/locals, exact typed Coordinator.runner, and transitive
// aliases. Method-value aliases are reported separately and rejected.
func findAttemptRunnerRunSites(files map[string]*ast.File) (sites []runCallSite, methodValueViolations []string) {
	aliases := collectPackageTypeAliases(files)
	fields := collectStructFieldTypes(files, aliases)

	for path, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fn := funcDisplayName(fd)
			env := map[string]runnerValueProv{}
			if fd.Recv != nil {
				for _, f := range fd.Recv.List {
					if provenanceFromRunnerType(f.Type, aliases) == runnerProvAttemptRunner {
						for _, n := range f.Names {
							if n != nil {
								env[n.Name] = runnerProvAttemptRunner
							}
						}
					}
				}
			}
			if fd.Type != nil && fd.Type.Params != nil {
				for _, f := range fd.Type.Params.List {
					if provenanceFromRunnerType(f.Type, aliases) == runnerProvAttemptRunner {
						for _, n := range f.Names {
							if n != nil {
								env[n.Name] = runnerProvAttemptRunner
							}
						}
					}
				}
			}
			s, mv := collectRunnerRunSites(path, fn, fd.Body, env, aliases, fields)
			sites = append(sites, s...)
			methodValueViolations = append(methodValueViolations, mv...)
		}
	}
	return sites, methodValueViolations
}

func provenanceFromRunnerType(expr ast.Expr, aliases map[string]string) runnerValueProv {
	under := resolveTypeString(expr, aliases)
	if under == "*attemptRunner" || under == "attemptRunner" {
		return runnerProvAttemptRunner
	}
	return runnerProvUnknown
}

func runnerExprProvenance(expr ast.Expr, env map[string]runnerValueProv, fields map[string]map[string]string) runnerValueProv {
	switch e := unwrapParen(expr).(type) {
	case *ast.Ident:
		if p, ok := env[e.Name]; ok {
			return p
		}
	case *ast.SelectorExpr:
		if e.Sel == nil || e.Sel.Name != "runner" {
			return runnerProvUnknown
		}
		recv, ok := e.X.(*ast.Ident)
		if !ok || recv.Name != "c" {
			return runnerProvUnknown
		}
		if ft := fields["Coordinator"]["runner"]; ft == "*attemptRunner" || ft == "attemptRunner" {
			return runnerProvAttemptRunner
		}
	}
	return runnerProvUnknown
}

func collectRunnerRunSites(
	path, fn string,
	body *ast.BlockStmt,
	env map[string]runnerValueProv,
	aliases map[string]string,
	fields map[string]map[string]string,
) (sites []runCallSite, methodValueViolations []string) {
	var walk func(body *ast.BlockStmt, env map[string]runnerValueProv)

	recordMethodValue := func(sel *ast.SelectorExpr, env map[string]runnerValueProv) {
		if sel == nil || sel.Sel == nil || sel.Sel.Name != "Run" {
			return
		}
		if runnerExprProvenance(sel.X, env, fields) != runnerProvAttemptRunner {
			return
		}
		methodValueViolations = append(methodValueViolations, fmt.Sprintf("%s: method-value alias of Run in %s", path, fn))
	}
	recordCall := func(call *ast.CallExpr, env map[string]runnerValueProv) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "Run" {
			return
		}
		if runnerExprProvenance(sel.X, env, fields) == runnerProvAttemptRunner {
			sites = append(sites, runCallSite{file: path, fn: fn})
		}
	}
	inspectExpr := func(expr ast.Expr, env map[string]runnerValueProv) {
		if expr == nil {
			return
		}
		ast.Inspect(expr, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				recordCall(call, env)
			}
			return true
		})
	}
	applyAssign := func(as *ast.AssignStmt, env map[string]runnerValueProv) {
		for _, rhs := range as.Rhs {
			if sel, ok := unwrapParen(rhs).(*ast.SelectorExpr); ok {
				recordMethodValue(sel, env)
			}
			inspectExpr(rhs, env)
		}
		for i, rhs := range as.Rhs {
			if i >= len(as.Lhs) {
				continue
			}
			lhs, ok := as.Lhs[i].(*ast.Ident)
			if !ok || lhs.Name == "_" {
				continue
			}
			if p := runnerExprProvenance(rhs, env, fields); p != runnerProvUnknown {
				env[lhs.Name] = p
			}
		}
	}
	applyValueSpec := func(vs *ast.ValueSpec, env map[string]runnerValueProv) {
		if vs.Type != nil {
			if p := provenanceFromRunnerType(vs.Type, aliases); p != runnerProvUnknown {
				for _, name := range vs.Names {
					if name != nil {
						env[name.Name] = p
					}
				}
			}
		}
		for i, name := range vs.Names {
			if name == nil || i >= len(vs.Values) {
				continue
			}
			if sel, ok := unwrapParen(vs.Values[i]).(*ast.SelectorExpr); ok {
				recordMethodValue(sel, env)
			}
			inspectExpr(vs.Values[i], env)
			if p := runnerExprProvenance(vs.Values[i], env, fields); p != runnerProvUnknown {
				env[name.Name] = p
			}
		}
	}
	walk = func(body *ast.BlockStmt, env map[string]runnerValueProv) {
		if body == nil {
			return
		}
		for _, stmt := range body.List {
			switch s := stmt.(type) {
			case *ast.AssignStmt:
				applyAssign(s, env)
			case *ast.DeclStmt:
				gd, ok := s.Decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range gd.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						applyValueSpec(vs, env)
					}
				}
			case *ast.DeferStmt:
				if s.Call == nil {
					continue
				}
				if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
					child := cloneRunnerProv(env)
					if lit.Type != nil && lit.Type.Params != nil {
						for _, f := range lit.Type.Params.List {
							if provenanceFromRunnerType(f.Type, aliases) == runnerProvAttemptRunner {
								for _, n := range f.Names {
									if n != nil {
										child[n.Name] = runnerProvAttemptRunner
									}
								}
							}
						}
					}
					walk(lit.Body, child)
					continue
				}
				inspectExpr(s.Call, env)
			case *ast.GoStmt:
				if s.Call == nil {
					continue
				}
				if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
					walk(lit.Body, cloneRunnerProv(env))
					continue
				}
				inspectExpr(s.Call, env)
			case *ast.ExprStmt:
				if sel, ok := unwrapParen(s.X).(*ast.SelectorExpr); ok {
					recordMethodValue(sel, env)
				}
				inspectExpr(s.X, env)
			case *ast.IfStmt:
				child := cloneRunnerProv(env)
				if s.Init != nil {
					if as, ok := s.Init.(*ast.AssignStmt); ok {
						applyAssign(as, child)
					}
				}
				inspectExpr(s.Cond, child)
				walk(s.Body, child)
				if s.Else != nil {
					elseEnv := cloneRunnerProv(env)
					switch e := s.Else.(type) {
					case *ast.BlockStmt:
						walk(e, elseEnv)
					case *ast.IfStmt:
						walk(&ast.BlockStmt{List: []ast.Stmt{e}}, elseEnv)
					}
				}
			case *ast.BlockStmt:
				walk(s, cloneRunnerProv(env))
			case *ast.ForStmt:
				child := cloneRunnerProv(env)
				if s.Init != nil {
					if as, ok := s.Init.(*ast.AssignStmt); ok {
						applyAssign(as, child)
					}
				}
				walk(s.Body, child)
			case *ast.RangeStmt:
				walk(s.Body, cloneRunnerProv(env))
			case *ast.ReturnStmt:
				for _, r := range s.Results {
					inspectExpr(r, env)
				}
			default:
				ast.Inspect(s, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.CallExpr:
						recordCall(x, env)
					case *ast.AssignStmt:
						applyAssign(x, env)
						return false
					}
					return true
				})
			}
		}
	}
	walk(body, env)
	return sites, methodValueViolations
}

func cloneRunnerProv(in map[string]runnerValueProv) map[string]runnerValueProv {
	out := make(map[string]runnerValueProv, len(in))
	maps.Copy(out, in)
	return out
}

type stageValueProv int

const (
	stageProvUnknown stageValueProv = iota
	stageProvSource
	stageProvLoader
	stageProvClassify
	stageProvCompile
	stageProvManager
)

// forbiddenCoordinatorReloadExecutionCalls scans coordinator*.go with resolved
// field/param/local provenance for detailed reload-stage execution that must
// live on attemptRunner. AbsolutePath and Manager status/shutdown remain OK.
func forbiddenCoordinatorReloadExecutionCalls(files map[string]*ast.File) []string {
	var violations []string
	aliases := collectPackageTypeAliases(files)
	fieldProv := collectStructFieldStageProv(files, aliases)

	for path, file := range files {
		if !strings.HasPrefix(path, "coordinator") {
			continue
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fn := funcDisplayName(fd)
			env := map[string]stageValueProv{}
			recvTypes := map[string]string{}
			if fd.Recv != nil {
				for _, f := range fd.Recv.List {
					typ := strings.TrimPrefix(resolveTypeString(f.Type, aliases), "*")
					for _, n := range f.Names {
						if n != nil {
							recvTypes[n.Name] = typ
						}
					}
				}
			}
			if fd.Type != nil && fd.Type.Params != nil {
				for _, f := range fd.Type.Params.List {
					p := stageProvFromTypeExpr(f.Type, aliases)
					if p == stageProvUnknown {
						continue
					}
					for _, n := range f.Names {
						if n != nil {
							env[n.Name] = p
						}
					}
				}
			}
			got := collectForbiddenStageCalls(path, fn, fd.Body, env, aliases, fieldProv, recvTypes)
			violations = append(violations, got...)
		}
	}
	return violations
}

func stageProvFromTypeString(typ string) stageValueProv {
	switch typ {
	case "StableConfigSource":
		return stageProvSource
	case "EffectiveLoader":
		return stageProvLoader
	case "CandidateCompiler":
		return stageProvCompile
	case "*Manager", "Manager":
		return stageProvManager
	default:
		return stageProvUnknown
	}
}

func stageProvFromTypeExpr(expr ast.Expr, aliases map[string]string) stageValueProv {
	if isClassifierFuncType(expr) {
		return stageProvClassify
	}
	return stageProvFromTypeString(resolveTypeString(expr, aliases))
}

func stageExprProvenance(
	expr ast.Expr,
	env map[string]stageValueProv,
	fieldProv map[string]map[string]stageValueProv,
	recvTypes map[string]string,
) stageValueProv {
	switch e := unwrapParen(expr).(type) {
	case *ast.Ident:
		if p, ok := env[e.Name]; ok {
			return p
		}
	case *ast.SelectorExpr:
		if e.Sel == nil {
			return stageProvUnknown
		}
		recv, ok := e.X.(*ast.Ident)
		if !ok {
			return stageProvUnknown
		}
		if typ, ok := recvTypes[recv.Name]; ok {
			if p, ok := fieldProv[typ][e.Sel.Name]; ok {
				return p
			}
		}
	}
	return stageProvUnknown
}

// collectInvokedStageRoles returns the set of detailed reload-stage roles
// invoked in body, reusing the same provenance engine as the Coordinator gate.
func collectInvokedStageRoles(
	body *ast.BlockStmt,
	env map[string]stageValueProv,
	fieldProv map[string]map[string]stageValueProv,
	recvTypes map[string]string,
) map[stageValueProv]bool {
	roles := map[stageValueProv]bool{}
	noteCall := func(call *ast.CallExpr, env map[string]stageValueProv) {
		if id, ok := unwrapParen(call.Fun).(*ast.Ident); ok {
			if env[id.Name] == stageProvClassify {
				roles[stageProvClassify] = true
			}
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return
		}
		// Receiver field invoke: s.cls(...) / c.classify(...)
		if recv, ok := sel.X.(*ast.Ident); ok {
			if typ, ok := recvTypes[recv.Name]; ok {
				if fieldProv[typ][sel.Sel.Name] == stageProvClassify {
					roles[stageProvClassify] = true
					return
				}
			}
		}
		prov := stageExprProvenance(sel.X, env, fieldProv, recvTypes)
		switch prov {
		case stageProvSource:
			if sel.Sel.Name == "ReadStable" {
				roles[stageProvSource] = true
			}
		case stageProvLoader:
			if sel.Sel.Name == "LoadEffective" {
				roles[stageProvLoader] = true
			}
		case stageProvCompile:
			if sel.Sel.Name == "Compile" {
				roles[stageProvCompile] = true
			}
		case stageProvManager:
			switch sel.Sel.Name {
			case "PrepareRequestPlane", "Publish":
				roles[stageProvManager] = true
			}
		case stageProvClassify:
			roles[stageProvClassify] = true
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for i, rhs := range x.Rhs {
				ast.Inspect(rhs, func(nn ast.Node) bool {
					if call, ok := nn.(*ast.CallExpr); ok {
						noteCall(call, env)
					}
					return true
				})
				if i < len(x.Lhs) {
					if lhs, ok := x.Lhs[i].(*ast.Ident); ok && lhs.Name != "_" {
						if p := stageExprProvenance(rhs, env, fieldProv, recvTypes); p != stageProvUnknown {
							env[lhs.Name] = p
						}
					}
				}
			}
			return false
		case *ast.ValueSpec:
			if x.Type != nil {
				if p := stageProvFromTypeExpr(x.Type, nil); p != stageProvUnknown {
					for _, name := range x.Names {
						if name != nil {
							env[name.Name] = p
						}
					}
				}
			}
			for i, name := range x.Names {
				if name == nil || i >= len(x.Values) {
					continue
				}
				ast.Inspect(x.Values[i], func(nn ast.Node) bool {
					if call, ok := nn.(*ast.CallExpr); ok {
						noteCall(call, env)
					}
					return true
				})
				if p := stageExprProvenance(x.Values[i], env, fieldProv, recvTypes); p != stageProvUnknown {
					env[name.Name] = p
				}
			}
			return false
		case *ast.CallExpr:
			noteCall(x, env)
		}
		return true
	})
	return roles
}

func collectForbiddenStageCalls(
	path, fn string,
	body *ast.BlockStmt,
	env map[string]stageValueProv,
	aliases map[string]string,
	fieldProv map[string]map[string]stageValueProv,
	recvTypes map[string]string,
) []string {
	var violations []string
	var walk func(body *ast.BlockStmt, env map[string]stageValueProv)

	recordCall := func(call *ast.CallExpr, env map[string]stageValueProv) {
		if id, ok := unwrapParen(call.Fun).(*ast.Ident); ok {
			if env[id.Name] == stageProvClassify {
				violations = append(violations, fmt.Sprintf("%s: %s calls classify (aliased) directly", path, fn))
			}
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return
		}
		// Receiver field invoke: c.classify(...)
		if recv, ok := sel.X.(*ast.Ident); ok {
			if typ, ok := recvTypes[recv.Name]; ok {
				if fieldProv[typ][sel.Sel.Name] == stageProvClassify {
					violations = append(violations, fmt.Sprintf("%s: %s calls classify field directly", path, fn))
					return
				}
			}
		}
		prov := stageExprProvenance(sel.X, env, fieldProv, recvTypes)
		switch prov {
		case stageProvSource:
			if sel.Sel.Name == "ReadStable" {
				violations = append(violations, fmt.Sprintf("%s: %s calls ReadStable on StableConfigSource provenance", path, fn))
			}
		case stageProvLoader:
			if sel.Sel.Name == "LoadEffective" {
				violations = append(violations, fmt.Sprintf("%s: %s calls LoadEffective on EffectiveLoader provenance", path, fn))
			}
		case stageProvCompile:
			if sel.Sel.Name == "Compile" {
				violations = append(violations, fmt.Sprintf("%s: %s calls Compile on CandidateCompiler provenance", path, fn))
			}
		case stageProvManager:
			switch sel.Sel.Name {
			case "PrepareRequestPlane", "Publish":
				violations = append(violations, fmt.Sprintf("%s: %s calls %s on Manager provenance", path, fn, sel.Sel.Name))
			}
		case stageProvClassify:
			violations = append(violations, fmt.Sprintf("%s: %s calls classify provenance", path, fn))
		}
	}

	inspectExpr := func(expr ast.Expr, env map[string]stageValueProv) {
		if expr == nil {
			return
		}
		ast.Inspect(expr, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				recordCall(call, env)
			}
			return true
		})
	}
	applyAssign := func(as *ast.AssignStmt, env map[string]stageValueProv) {
		for _, rhs := range as.Rhs {
			inspectExpr(rhs, env)
		}
		for i, rhs := range as.Rhs {
			if i >= len(as.Lhs) {
				continue
			}
			lhs, ok := as.Lhs[i].(*ast.Ident)
			if !ok || lhs.Name == "_" {
				continue
			}
			if p := stageExprProvenance(rhs, env, fieldProv, recvTypes); p != stageProvUnknown {
				env[lhs.Name] = p
			}
		}
	}
	applyValueSpec := func(vs *ast.ValueSpec, env map[string]stageValueProv) {
		if vs.Type != nil {
			if p := stageProvFromTypeExpr(vs.Type, aliases); p != stageProvUnknown {
				for _, name := range vs.Names {
					if name != nil {
						env[name.Name] = p
					}
				}
			}
		}
		for i, name := range vs.Names {
			if name == nil || i >= len(vs.Values) {
				continue
			}
			inspectExpr(vs.Values[i], env)
			if p := stageExprProvenance(vs.Values[i], env, fieldProv, recvTypes); p != stageProvUnknown {
				env[name.Name] = p
			}
		}
	}
	walk = func(body *ast.BlockStmt, env map[string]stageValueProv) {
		if body == nil {
			return
		}
		for _, stmt := range body.List {
			switch s := stmt.(type) {
			case *ast.AssignStmt:
				applyAssign(s, env)
			case *ast.DeclStmt:
				gd, ok := s.Decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range gd.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						applyValueSpec(vs, env)
					}
				}
			case *ast.DeferStmt:
				if s.Call == nil {
					continue
				}
				if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
					child := cloneStageProv(env)
					if lit.Type != nil && lit.Type.Params != nil {
						for _, f := range lit.Type.Params.List {
							p := stageProvFromTypeExpr(f.Type, aliases)
							if p == stageProvUnknown {
								continue
							}
							for _, n := range f.Names {
								if n != nil {
									child[n.Name] = p
								}
							}
						}
					}
					walk(lit.Body, child)
					continue
				}
				inspectExpr(s.Call, env)
			case *ast.GoStmt:
				if s.Call == nil {
					continue
				}
				if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
					walk(lit.Body, cloneStageProv(env))
					continue
				}
				inspectExpr(s.Call, env)
			case *ast.ExprStmt:
				inspectExpr(s.X, env)
			case *ast.IfStmt:
				child := cloneStageProv(env)
				if s.Init != nil {
					if as, ok := s.Init.(*ast.AssignStmt); ok {
						applyAssign(as, child)
					}
				}
				inspectExpr(s.Cond, child)
				walk(s.Body, child)
				if s.Else != nil {
					elseEnv := cloneStageProv(env)
					switch e := s.Else.(type) {
					case *ast.BlockStmt:
						walk(e, elseEnv)
					case *ast.IfStmt:
						walk(&ast.BlockStmt{List: []ast.Stmt{e}}, elseEnv)
					}
				}
			case *ast.BlockStmt:
				walk(s, cloneStageProv(env))
			case *ast.ForStmt:
				walk(s.Body, cloneStageProv(env))
			case *ast.RangeStmt:
				walk(s.Body, cloneStageProv(env))
			case *ast.ReturnStmt:
				for _, r := range s.Results {
					inspectExpr(r, env)
				}
			default:
				ast.Inspect(s, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.CallExpr:
						recordCall(x, env)
					case *ast.AssignStmt:
						applyAssign(x, env)
						return false
					}
					return true
				})
			}
		}
	}
	walk(body, env)
	return violations
}

func cloneStageProv(in map[string]stageValueProv) map[string]stageValueProv {
	out := make(map[string]stageValueProv, len(in))
	maps.Copy(out, in)
	return out
}

// scanRunnerFileForForbiddenOwnership proves attempt_runner.go never declares
// an attemptGate/attemptLease-typed field, never calls a gate transition
// method, and never calls recordTerminal (Coordinator-owned terminal
// recording, Task 6.4 boundary).
func scanRunnerFileForForbiddenOwnership(file *ast.File) []string {
	var violations []string
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			for _, f := range st.Fields.List {
				typ := typeExprString(f.Type)
				if typ == "attemptGate" || typ == "*attemptGate" ||
					typ == "attemptLease" || typ == "*attemptLease" {
					violations = append(violations, "attempt_runner.go: field of forbidden type "+typ)
				}
			}
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return true
		}
		if isGateTransitionMethod(sel.Sel.Name) {
			violations = append(violations, "attempt_runner.go: forbidden gate transition call "+sel.Sel.Name)
		}
		if sel.Sel.Name == "recordTerminal" {
			violations = append(violations, "attempt_runner.go: forbidden recordTerminal call")
		}
		return true
	})
	return violations
}
