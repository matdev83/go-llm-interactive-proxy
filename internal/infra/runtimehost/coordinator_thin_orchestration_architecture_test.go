package runtimehost

// Task 6.5 permanent architecture enforcement: Coordinator is thin orchestration
// composing gate, runner, state, and observer. coordinator.go stays ≤300 lines
// (Req 11.3 final ceiling) and at the exact Task 6.5 current ratchet (292).
// Three-source equality (actual / CriticalFileBudgets.Max / freeze CurrentMax)
// is owned by internal/archtest. This file owns method-location gates and the
// local line ceiling/exact checks. Existing Task 6.2–6.4 gates remain
// authoritative for gate/runner/state ownership graphs.

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"strings"
	"testing"
)

// coordinatorOrchestrationMethods must remain declared on Coordinator in
// coordinator.go (Req 6.4 / Task 6.5). FixedSourcePath is the sole pre-existing
// exception and lives in coordinator_fixed_source.go.
var coordinatorOrchestrationMethods = []string{
	"Reload",
	"Status",
	"BeginShutdown",
	"WaitForIdle",
	"refreshGauges",
	"terminal",
	"shuttingDown",
	"activeGenerationID",
}

// coordinatorPortSeamNames are construction/value seams that may live in one
// focused production file outside coordinator.go, but must not carry
// Coordinator mutable state or orchestration methods.
var coordinatorPortSeamNames = []string{
	"DefaultReloadTimeout",
	"StableConfigSource",
	"EffectiveLoader",
	"CandidateCompiler",
	"BackendFactoryKindCounter",
	"CoordinatorDeps",
	"FuncEffectiveLoader",
	"FuncCompiler",
}

const (
	coordinatorThinOrchestrationLineCeiling = 300
	// Exact Task 6.5 accepted physical total. Authoritative three-source sync
	// (file / CriticalFileBudgets / freeze CurrentMax) lives in archtest.
	coordinatorThinOrchestrationExactCurrent = 292
)

// TestCoordinatorThinOrchestration_LineCeiling permanently certifies
// coordinator.go is at most 300 physical lines (Task 6.5 / Req 11.3) and
// equals the exact Task 6.5 current ratchet (not a padded ceiling).
func TestCoordinatorThinOrchestration_LineCeiling(t *testing.T) {
	t.Parallel()
	n, err := countRuntimehostFileLines("coordinator.go")
	if err != nil {
		t.Fatalf("coordinator.go: %v", err)
	}
	if n > coordinatorThinOrchestrationLineCeiling {
		t.Fatalf("coordinator.go has %d physical lines; Task 6.5 requires at most %d",
			n, coordinatorThinOrchestrationLineCeiling)
	}
	if n != coordinatorThinOrchestrationExactCurrent {
		t.Fatalf("coordinator.go has %d physical lines; Task 6.5 exact current ratchet is %d (silent shrink/pad must update archtest metadata together)",
			n, coordinatorThinOrchestrationExactCurrent)
	}
}

// TestCoordinatorThinOrchestration_CanonicalMethodLocations proves production
// Coordinator orchestration methods (and NewCoordinator / Coordinator struct)
// remain in coordinator.go, FixedSourcePath stays in its focused file, and no
// other file hosts Coordinator orchestration methods.
func TestCoordinatorThinOrchestration_CanonicalMethodLocations(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	files := parseProductionRuntimehostFiles(t, fset)
	if got := analyzeCoordinatorThinOrchestration(files); len(got) > 0 {
		t.Fatalf("Coordinator thin-orchestration violations:\n%s", strings.Join(got, "\n"))
	}
}

// TestCoordinatorThinOrchestration_SyntheticFixtures locks positive gaming
// detections and negative controls without duplicating Task 6.2–6.4 graphs.
func TestCoordinatorThinOrchestration_SyntheticFixtures(t *testing.T) {
	t.Parallel()

	canonical := map[string]string{
		"coordinator.go": `
package runtimehost
type Coordinator struct {
	gate *attemptGate
	runner *attemptRunner
	state *ReloadState
	observer *ReloadObserver
	mgr *Manager
}
func NewCoordinator() *Coordinator { return &Coordinator{} }
func (c *Coordinator) Reload() {}
func (c *Coordinator) Status() {}
func (c *Coordinator) BeginShutdown() {}
func (c *Coordinator) WaitForIdle() {}
func (c *Coordinator) refreshGauges() {}
func (c *Coordinator) terminal() {}
func (c *Coordinator) shuttingDown() bool { return false }
func (c *Coordinator) activeGenerationID() int64 { return 0 }
`,
		"coordinator_fixed_source.go": `
package runtimehost
func (c *Coordinator) FixedSourcePath() string { return "" }
`,
		"reload_ports.go": `
package runtimehost
const DefaultReloadTimeout = 1
type StableConfigSource interface{ AbsolutePath() string }
type EffectiveLoader interface{ LoadEffective() }
type CandidateCompiler interface{ Compile() }
type BackendFactoryKindCounter interface{ BackendFactoryKindCounts() }
type CoordinatorDeps struct{ Source StableConfigSource }
type FuncEffectiveLoader func()
func (f FuncEffectiveLoader) LoadEffective() {}
type FuncCompiler func()
func (f FuncCompiler) Compile() {}
`,
		"reload_state.go": `
package runtimehost
type ReloadState struct{}
func cloneActiveSource() {}
`,
		"attempt_gate.go": `package runtimehost
type attemptGate struct{}
`,
		"attempt_runner.go": `package runtimehost
type attemptRunner struct{}
`,
		"observability.go": `package runtimehost
type ReloadObserver struct{}
`,
		"generation.go": `package runtimehost
type Manager struct{}
`,
	}

	t.Run("accepts_canonical_thin_layout", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, canonical)
		if got := analyzeCoordinatorThinOrchestration(files); len(got) > 0 {
			t.Fatalf("canonical thin layout must be accepted; got %v", got)
		}
	})

	t.Run("rejects_Reload_moved_out_of_coordinator", func(t *testing.T) {
		t.Parallel()
		src := cloneStringMap(canonical)
		src["coordinator.go"] = strings.Replace(src["coordinator.go"], "func (c *Coordinator) Reload() {}\n", "", 1)
		src["elsewhere.go"] = `
package runtimehost
func (c *Coordinator) Reload() {}
`
		files := mustParseSyntheticFiles(t, src)
		got := analyzeCoordinatorThinOrchestration(files)
		if !violationContains(got, "Reload") {
			t.Fatalf("expected Reload relocation rejection; got %v", got)
		}
	})

	t.Run("rejects_Status_moved_out_of_coordinator", func(t *testing.T) {
		t.Parallel()
		src := cloneStringMap(canonical)
		src["coordinator.go"] = strings.Replace(src["coordinator.go"], "func (c *Coordinator) Status() {}\n", "", 1)
		src["status_elsewhere.go"] = `
package runtimehost
func (c *Coordinator) Status() {}
`
		files := mustParseSyntheticFiles(t, src)
		got := analyzeCoordinatorThinOrchestration(files)
		if !violationContains(got, "Status") {
			t.Fatalf("expected Status relocation rejection; got %v", got)
		}
	})

	t.Run("rejects_BeginShutdown_moved_out_of_coordinator", func(t *testing.T) {
		t.Parallel()
		src := cloneStringMap(canonical)
		src["coordinator.go"] = strings.Replace(src["coordinator.go"], "func (c *Coordinator) BeginShutdown() {}\n", "", 1)
		src["shutdown_elsewhere.go"] = `
package runtimehost
func (c *Coordinator) BeginShutdown() {}
`
		files := mustParseSyntheticFiles(t, src)
		got := analyzeCoordinatorThinOrchestration(files)
		if !violationContains(got, "BeginShutdown") {
			t.Fatalf("expected BeginShutdown relocation rejection; got %v", got)
		}
	})

	t.Run("rejects_WaitForIdle_moved_out_of_coordinator", func(t *testing.T) {
		t.Parallel()
		src := cloneStringMap(canonical)
		src["coordinator.go"] = strings.Replace(src["coordinator.go"], "func (c *Coordinator) WaitForIdle() {}\n", "", 1)
		src["idle_elsewhere.go"] = `
package runtimehost
func (c *Coordinator) WaitForIdle() {}
`
		files := mustParseSyntheticFiles(t, src)
		got := analyzeCoordinatorThinOrchestration(files)
		if !violationContains(got, "WaitForIdle") {
			t.Fatalf("expected WaitForIdle relocation rejection; got %v", got)
		}
	})

	t.Run("rejects_orchestration_helper_moved_out_of_coordinator", func(t *testing.T) {
		t.Parallel()
		src := cloneStringMap(canonical)
		src["coordinator.go"] = strings.Replace(src["coordinator.go"], "func (c *Coordinator) refreshGauges() {}\n", "", 1)
		src["helper_elsewhere.go"] = `
package runtimehost
func (c *Coordinator) refreshGauges() {}
`
		files := mustParseSyntheticFiles(t, src)
		got := analyzeCoordinatorThinOrchestration(files)
		if !violationContains(got, "refreshGauges") {
			t.Fatalf("expected refreshGauges relocation rejection; got %v", got)
		}
	})

	t.Run("rejects_ports_still_declared_in_coordinator", func(t *testing.T) {
		t.Parallel()
		src := cloneStringMap(canonical)
		src["coordinator.go"] += `
const DefaultReloadTimeout = 2
type StableConfigSource interface{ AbsolutePath() string }
`
		delete(src, "reload_ports.go")
		files := mustParseSyntheticFiles(t, src)
		got := analyzeCoordinatorThinOrchestration(files)
		if !violationContains(got, "DefaultReloadTimeout") && !violationContains(got, "StableConfigSource") {
			t.Fatalf("expected ports-in-coordinator rejection; got %v", got)
		}
	})

	t.Run("rejects_cloneActiveSource_in_coordinator", func(t *testing.T) {
		t.Parallel()
		src := cloneStringMap(canonical)
		src["coordinator.go"] += `
func cloneActiveSource() {}
`
		src["reload_state.go"] = `
package runtimehost
type ReloadState struct{}
`
		files := mustParseSyntheticFiles(t, src)
		got := analyzeCoordinatorThinOrchestration(files)
		if !violationContains(got, "cloneActiveSource") {
			t.Fatalf("expected cloneActiveSource-in-coordinator rejection; got %v", got)
		}
	})

	t.Run("rejects_scattered_port_seams", func(t *testing.T) {
		t.Parallel()
		src := cloneStringMap(canonical)
		src["ports_a.go"] = `
package runtimehost
const DefaultReloadTimeout = 1
type StableConfigSource interface{ AbsolutePath() string }
`
		src["ports_b.go"] = `
package runtimehost
type EffectiveLoader interface{ LoadEffective() }
type CoordinatorDeps struct{}
`
		delete(src, "reload_ports.go")
		files := mustParseSyntheticFiles(t, src)
		got := analyzeCoordinatorThinOrchestration(files)
		if !violationContains(got, "port seam") && !violationContains(got, "focused") {
			t.Fatalf("expected scattered port-seam rejection; got %v", got)
		}
	})

	t.Run("rejects_Coordinator_method_on_ports_file", func(t *testing.T) {
		t.Parallel()
		src := cloneStringMap(canonical)
		src["reload_ports.go"] += `
func (c *Coordinator) sneaky() {}
`
		files := mustParseSyntheticFiles(t, src)
		got := analyzeCoordinatorThinOrchestration(files)
		if !violationContains(got, "sneaky") {
			t.Fatalf("expected Coordinator method on ports file rejection; got %v", got)
		}
	})

	t.Run("accepts_FixedSourcePath_in_focused_file", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, canonical)
		got := analyzeCoordinatorThinOrchestration(files)
		if violationContains(got, "FixedSourcePath") {
			t.Fatalf("FixedSourcePath in coordinator_fixed_source.go must be allowed; got %v", got)
		}
	})

	t.Run("accepts_unrelated_receiver_methods_outside_coordinator", func(t *testing.T) {
		t.Parallel()
		src := cloneStringMap(canonical)
		src["other.go"] = `
package runtimehost
type Unrelated struct{}
func (u *Unrelated) Help() {}
func helperFree() {}
`
		files := mustParseSyntheticFiles(t, src)
		if got := analyzeCoordinatorThinOrchestration(files); len(got) > 0 {
			t.Fatalf("unrelated receivers must be allowed; got %v", got)
		}
	})

	t.Run("accepts_focused_port_interfaces_outside_coordinator", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, canonical)
		got := analyzeCoordinatorThinOrchestration(files)
		if violationContains(got, "StableConfigSource") || violationContains(got, "CoordinatorDeps") {
			t.Fatalf("focused ports outside coordinator.go must be allowed; got %v", got)
		}
	})
}

func analyzeCoordinatorThinOrchestration(files map[string]*ast.File) []string {
	var violations []string

	coordFiles := findStructDeclFiles(files, "Coordinator")
	if len(coordFiles) != 1 || coordFiles[0] != "coordinator.go" {
		violations = append(violations, fmt.Sprintf("want exactly one Coordinator struct in coordinator.go; got %v", coordFiles))
	}

	newCoordFiles := findFuncDeclFiles(files, "NewCoordinator", "")
	if len(newCoordFiles) != 1 || newCoordFiles[0] != "coordinator.go" {
		violations = append(violations, fmt.Sprintf("want NewCoordinator only in coordinator.go; got %v", newCoordFiles))
	}

	for _, name := range coordinatorOrchestrationMethods {
		locs := findFuncDeclFiles(files, name, "Coordinator")
		switch {
		case len(locs) == 0:
			violations = append(violations, fmt.Sprintf("missing Coordinator.%s in coordinator.go", name))
		case len(locs) != 1 || locs[0] != "coordinator.go":
			violations = append(violations, fmt.Sprintf("Coordinator.%s must live only in coordinator.go; got %v", name, locs))
		}
	}

	fixedLocs := findFuncDeclFiles(files, "FixedSourcePath", "Coordinator")
	switch {
	case len(fixedLocs) == 0:
		violations = append(violations, "missing Coordinator.FixedSourcePath in coordinator_fixed_source.go")
	case len(fixedLocs) != 1 || fixedLocs[0] != "coordinator_fixed_source.go":
		violations = append(violations, fmt.Sprintf("FixedSourcePath must live only in coordinator_fixed_source.go; got %v", fixedLocs))
	}

	for path, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Recv == nil || len(fd.Recv.List) == 0 {
				continue
			}
			if receiverTypeName(fd.Recv.List[0].Type) != "Coordinator" {
				continue
			}
			name := fd.Name.Name
			if name == "FixedSourcePath" {
				if path != "coordinator_fixed_source.go" {
					violations = append(violations, fmt.Sprintf("%s: Coordinator.%s must not leave coordinator_fixed_source.go", path, name))
				}
				continue
			}
			if path != "coordinator.go" {
				violations = append(violations, fmt.Sprintf("%s: Coordinator orchestration method %s must remain in coordinator.go", path, name))
			}
		}
	}

	portFiles := map[string]struct{}{}
	for path, file := range files {
		for _, name := range coordinatorPortSeamNames {
			if fileDeclaresTopLevelName(file, name) {
				portFiles[path] = struct{}{}
				if path == "coordinator.go" {
					violations = append(violations, fmt.Sprintf("coordinator.go must not declare reload port/seam %s", name))
				}
			}
		}
	}
	if len(portFiles) > 1 {
		var names []string
		for p := range portFiles {
			names = append(names, p)
		}
		violations = append(violations, fmt.Sprintf("reload port seams must live in one focused production file; got %v", names))
	}

	cloneLocs := findFuncDeclFiles(files, "cloneActiveSource", "")
	switch {
	case len(cloneLocs) == 0:
		violations = append(violations, "missing cloneActiveSource next to ReloadState usage")
	case len(cloneLocs) != 1 || cloneLocs[0] != "reload_state.go":
		violations = append(violations, fmt.Sprintf("cloneActiveSource must live only in reload_state.go; got %v", cloneLocs))
	}

	if file, ok := files["coordinator.go"]; ok {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok == token.TYPE {
					for _, spec := range d.Specs {
						ts, ok := spec.(*ast.TypeSpec)
						if !ok || ts.Name == nil {
							continue
						}
						if ts.Name.Name != "Coordinator" {
							violations = append(violations, fmt.Sprintf("coordinator.go must not declare non-orchestration type %s", ts.Name.Name))
						}
					}
				}
				if d.Tok == token.CONST || d.Tok == token.VAR {
					for _, spec := range d.Specs {
						vs, ok := spec.(*ast.ValueSpec)
						if !ok {
							continue
						}
						for _, n := range vs.Names {
							violations = append(violations, fmt.Sprintf("coordinator.go must not declare const/var %s (ports/seams belong outside)", n.Name))
						}
					}
				}
			case *ast.FuncDecl:
				if d.Name == nil {
					continue
				}
				if d.Recv == nil || len(d.Recv.List) == 0 {
					if d.Name.Name != "NewCoordinator" {
						violations = append(violations, fmt.Sprintf("coordinator.go must not declare free function %s", d.Name.Name))
					}
					continue
				}
				if receiverTypeName(d.Recv.List[0].Type) != "Coordinator" {
					violations = append(violations, fmt.Sprintf("coordinator.go must not declare non-Coordinator method %s", d.Name.Name))
				}
			}
		}
	}

	return violations
}

func findStructDeclFiles(files map[string]*ast.File, name string) []string {
	var out []string
	for path, file := range files {
		ts := findTypeSpec(file, name)
		if ts == nil {
			continue
		}
		if _, ok := ts.Type.(*ast.StructType); ok && ts.Assign == 0 {
			out = append(out, path)
		}
	}
	return out
}

func findFuncDeclFiles(files map[string]*ast.File, name, recv string) []string {
	var out []string
	for path, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Name.Name != name {
				continue
			}
			if recv == "" {
				if fd.Recv == nil || len(fd.Recv.List) == 0 {
					out = append(out, path)
				}
				continue
			}
			if fd.Recv == nil || len(fd.Recv.List) == 0 {
				continue
			}
			if receiverTypeName(fd.Recv.List[0].Type) == recv {
				out = append(out, path)
			}
		}
	}
	return out
}

func fileDeclaresTopLevelName(file *ast.File, name string) bool {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name != nil && s.Name.Name == name {
						return true
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.Name == name {
							return true
						}
					}
				}
			}
		case *ast.FuncDecl:
			if d.Name != nil && d.Name.Name == name && (d.Recv == nil || len(d.Recv.List) == 0) {
				return true
			}
			// Method receivers named after adapter types (FuncEffectiveLoader.LoadEffective)
			// still count the type as declared via GenDecl; nothing else needed here.
		}
	}
	return false
}

func countRuntimehostFileLines(name string) (int, error) {
	f, err := os.Open(name)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	n := 0
	for sc.Scan() {
		n++
	}
	return n, sc.Err()
}
