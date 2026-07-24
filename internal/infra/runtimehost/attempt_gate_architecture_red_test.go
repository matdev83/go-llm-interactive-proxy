package runtimehost

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAttemptGate_ContractSemanticsDocumented locks the Task 6.2 semantic table
// and requires every row to declare characterization, AST assertion, or Task 6.2
// contract evidence — prose alone is not coverage.
func TestAttemptGate_ContractSemanticsDocumented(t *testing.T) {
	t.Parallel()
	want := map[string]bool{
		"atomic_arm":                               false,
		"api_busy_no_queue":                        false,
		"sighup_pending_once":                      false,
		"sighup_coalesce_count":                    false,
		"finish_claim_pending_followup":            false,
		"shutdown_atomic":                          false,
		"finish_exactly_once":                      false,
		"abandon_release_no_followup":              false,
		"caller_cancel_detach":                     false,
		"idle_wait_cases":                          false,
		"interleave_one_complete_vs_wait_shutdown": false,
	}
	allowedEvidence := map[attemptGateSemanticEvidenceKind]bool{
		evidenceCharacterization: true,
		evidenceASTRED:           true,
		evidenceTask62Contract:   true,
	}
	for _, row := range attemptGateContractSemantics {
		if row.Name == "" || row.Rule == "" {
			t.Fatalf("empty contract row: %+v", row)
		}
		if row.CoveredBy == "" {
			t.Fatalf("semantic %q missing CoveredBy mapping", row.Name)
		}
		if len(row.Evidence) == 0 {
			t.Fatalf("semantic %q missing Evidence kinds", row.Name)
		}
		for _, ev := range row.Evidence {
			if !allowedEvidence[ev] {
				t.Fatalf("semantic %q has unknown evidence %q", row.Name, ev)
			}
		}
		if _, ok := want[row.Name]; !ok {
			t.Fatalf("unexpected contract semantic %q", row.Name)
		}
		want[row.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("missing contract semantic %q", name)
		}
	}

	_ = []attemptAdmissionKind{
		admissionAdmitted,
		admissionBusyAPI,
		admissionPendingHUP,
		admissionCoalescedHUP,
		admissionRejectedShutdown,
	}
	_ = []attemptFinishKind{
		finishReleasedIdle,
		finishFollowUpClaimed,
		finishAlreadyCompleted,
	}
	_ = attemptAdmissionOutcome{}
	_ = attemptFinishOutcome{}
	var _ attemptGateContract
	var _ attemptLeaseContract
}

// TestAttemptGate_SemanticEvidenceMapped verifies CoveredBy pointers resolve to
// real Test* functions in this package or named contract vocabulary symbols.
func TestAttemptGate_SemanticEvidenceMapped(t *testing.T) {
	t.Parallel()
	tests := collectAttemptGatePackageTestNames(t)
	for _, row := range attemptGateContractSemantics {
		for _, part := range strings.Split(row.CoveredBy, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				t.Fatalf("empty CoveredBy fragment in %q", row.Name)
			}
			if strings.HasPrefix(part, "Test") {
				if !tests[part] {
					t.Fatalf("semantic %q CoveredBy %q: test not found in package", row.Name, part)
				}
				continue
			}
			switch part {
			case "attemptFinishOutcome",
				"attemptAdmissionOutcome",
				"attemptGateContract",
				"attemptLeaseContract":
			default:
				t.Fatalf("semantic %q CoveredBy %q: expected Test* or known contract symbol", row.Name, part)
			}
		}
	}
}

func collectAttemptGatePackageTestNames(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	out := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, "_test.go") {
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
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Name == nil {
				continue
			}
			if strings.HasPrefix(fd.Name.Name, "Test") {
				out[fd.Name.Name] = true
			}
		}
	}
	return out
}

// TestAttemptGate_ArchitectureOwnerAssertions permanently asserts Task 6.2
// ownership: exactly one production attemptGate owner, Coordinator no longer
// declares old gate fields/helpers, exact admission/lease dataflow, exact
// package caller graph, no polling on gate/Coordinator idle paths, and
// completion/abandon close only through one lock-owned release helper.
func TestAttemptGate_ArchitectureOwnerAssertions(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()

	files := parseProductionRuntimehostFiles(t, fset)
	violations := analyzeGateOwnership(files)
	if len(violations) > 0 {
		t.Fatalf("gate ownership violations:\n%s", strings.Join(violations, "\n"))
	}
	if got := analyzeGateCallerGraph(files); len(got) > 0 {
		t.Fatalf("gate caller graph violations:\n%s", strings.Join(got, "\n"))
	}
	if got := analyzeIdleCloseOwnership(files); len(got) > 0 {
		t.Fatalf("idle close ownership violations:\n%s", strings.Join(got, "\n"))
	}

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
	fields := map[string]bool{}
	for _, f := range st.Fields.List {
		for _, name := range f.Names {
			fields[name.Name] = true
		}
	}
	forbidden := []string{
		"busy",
		"pendingSignal",
		"coalesced",
		"attemptCancel",
		"attemptDone",
		"attemptOnce",
		"shutdown",
	}
	for _, name := range forbidden {
		if fields[name] {
			t.Fatalf("Coordinator must not declare gate field %q after Task 6.2", name)
		}
	}
	if !fields["gate"] {
		t.Fatal("Coordinator must own a gate field delegating admission/idle/shutdown")
	}

	for _, name := range []string{"armAttempt", "releaseAttempt"} {
		if findMethod(file, "Coordinator", name) != nil {
			t.Fatalf("Coordinator must not declare helper %s after Task 6.2", name)
		}
	}
	for _, name := range []string{"BeginShutdown", "WaitForIdle", "Reload", "Status"} {
		if findMethod(file, "Coordinator", name) == nil {
			t.Fatalf("missing Coordinator.%s", name)
		}
	}

	reload := findMethod(file, "Coordinator", "Reload")
	if reload == nil || reload.Body == nil {
		t.Fatal("Reload missing body")
	}
	if !coordinatorCallsExactGateMethod(reload.Body, "TryStart") {
		t.Fatal("Coordinator.Reload must call c.gate.TryStart (exact gate receiver)")
	}
	flow := analyzeReloadLeaseFlow(reload.Body)
	if !flow.ok {
		t.Fatalf("Coordinator.Reload lease flow violations:\n%s", strings.Join(flow.violations, "\n"))
	}
	if !reloadAbandonDeferBeforeRunAttempt(reload.Body) {
		t.Fatal("Coordinator.Reload must install deferred lease Abandon before runAttempt/post-admission work")
	}
	if reloadClaimsPendingFollowUpInline(reload.Body) {
		t.Fatal("Coordinator.Reload must not inline pending-HUP claim + coalesced reset")
	}
	if busyAssignmentExists(reload.Body) {
		t.Fatal("Coordinator.Reload must not assign busy=true; gate owns atomic arm")
	}

	wait := findMethod(file, "Coordinator", "WaitForIdle")
	if wait == nil || wait.Body == nil {
		t.Fatal("WaitForIdle missing body")
	}
	if !coordinatorCallsExactGateMethod(wait.Body, "WaitForIdle") {
		t.Fatal("Coordinator.WaitForIdle must call c.gate.WaitForIdle")
	}
	if funcContainsPollingTimer(wait.Body) {
		t.Fatal("Coordinator.WaitForIdle must not poll/sleep/ticker")
	}

	begin := findMethod(file, "Coordinator", "BeginShutdown")
	if begin == nil || begin.Body == nil {
		t.Fatal("BeginShutdown missing body")
	}
	if !coordinatorCallsExactGateMethod(begin.Body, "BeginShutdown") {
		t.Fatal("Coordinator.BeginShutdown must call c.gate.BeginShutdown")
	}

	status := findMethod(file, "Coordinator", "Status")
	if status == nil || status.Body == nil {
		t.Fatal("Status missing body")
	}
	if !coordinatorCallsExactGateMethod(status.Body, "Snapshot") {
		t.Fatal("Coordinator.Status must call c.gate.Snapshot")
	}

	gateFile := files["attempt_gate.go"]
	if gateFile == nil {
		t.Fatal("attempt_gate.go missing")
	}
	gateWait := findMethod(gateFile, "attemptGate", "WaitForIdle")
	if gateWait == nil || gateWait.Body == nil {
		t.Fatal("attemptGate.WaitForIdle missing")
	}
	if funcContainsPollingTimer(gateWait.Body) {
		t.Fatal("attemptGate.WaitForIdle must not poll/sleep/ticker")
	}
	if findMethod(gateFile, "attemptGate", canonicalReleaseHelper) == nil {
		t.Fatalf("missing canonical release helper %s", canonicalReleaseHelper)
	}
	complete := findMethod(gateFile, "attemptLease", "Complete")
	if complete == nil || complete.Body == nil {
		t.Fatal("attemptLease.Complete missing")
	}
	abandon := findMethod(gateFile, "attemptLease", "Abandon")
	if abandon == nil || abandon.Body == nil {
		t.Fatal("attemptLease.Abandon missing")
	}
}

// TestAttemptGate_CallerCancelDetachmentEvidence locks caller-cancel detachment:
// production TryStart uses context.WithoutCancel, and the focused behavioral
// proof test exists.
func TestAttemptGate_CallerCancelDetachmentEvidence(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	src, err := os.ReadFile("attempt_gate.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(fset, "attempt_gate.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	try := findMethod(file, "attemptGate", "TryStart")
	if try == nil || try.Body == nil {
		t.Fatal("attemptGate.TryStart missing")
	}
	if !funcContainsWithoutCancel(try.Body) {
		t.Fatal("expected attemptGate.TryStart to call context.WithoutCancel for host-owned attempt detachment (req 6.10)")
	}
	tests := collectAttemptGatePackageTestNames(t)
	if !tests["TestCoordinator_HostTimeoutIndependentOfClientCancel"] {
		t.Fatal("expected existing TestCoordinator_HostTimeoutIndependentOfClientCancel as behavioral detachment proof")
	}
	if !tests["TestAttemptGate_CallerCancelDetachment"] {
		t.Fatal("expected TestAttemptGate_CallerCancelDetachment as isolated gate detachment proof")
	}
}

func findTypeSpec(file *ast.File, name string) *ast.TypeSpec {
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if ok && ts.Name != nil && ts.Name.Name == name {
				return ts
			}
		}
	}
	return nil
}

func findMethod(file *ast.File, recv, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != name || fd.Recv == nil || len(fd.Recv.List) == 0 {
			continue
		}
		if receiverTypeName(fd.Recv.List[0].Type) == recv {
			return fd
		}
	}
	return nil
}

func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name
	default:
		return ""
	}
}

func funcContainsPollingTimer(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "time" {
			return true
		}
		switch sel.Sel.Name {
		case "After", "NewTicker", "Tick", "Sleep", "AfterFunc", "NewTimer":
			found = true
			return false
		}
		return true
	})
	return found
}

func funcContainsWithoutCancel(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "context" {
			return true
		}
		if sel.Sel.Name == "WithoutCancel" {
			found = true
			return false
		}
		return true
	})
	return found
}

func busyAssignmentExists(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range as.Lhs {
			sel, isSel := lhs.(*ast.SelectorExpr)
			if !isSel || sel.Sel == nil || sel.Sel.Name != "busy" {
				continue
			}
			if i < len(as.Rhs) {
				if lit, isLit := as.Rhs[i].(*ast.Ident); isLit && lit.Name == "true" {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// reloadClaimsPendingFollowUpInline detects an inlined Reload loop that clears
// pendingSignal and resets coalesced while still owning the active attempt slot.
func reloadClaimsPendingFollowUpInline(body *ast.BlockStmt) bool {
	clearsPending := false
	resetsCoalesced := false
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range as.Lhs {
			sel, isSel := lhs.(*ast.SelectorExpr)
			if !isSel || sel.Sel == nil || i >= len(as.Rhs) {
				continue
			}
			switch sel.Sel.Name {
			case "pendingSignal":
				if lit, isLit := as.Rhs[i].(*ast.Ident); isLit && lit.Name == "false" {
					clearsPending = true
				}
			case "coalesced":
				if lit, isLit := as.Rhs[i].(*ast.BasicLit); isLit && lit.Kind == token.INT && lit.Value == "0" {
					resetsCoalesced = true
				}
			}
		}
		return true
	})
	return clearsPending && resetsCoalesced
}

// TestAttemptGate_NoPollPolicyInConcurrencySuite rejects wall-clock polling and
// scheduler steering in attempt_gate_* tests and production idle paths.
// A past time.Time used only to build an already-expired context deadline is allowed.
func TestAttemptGate_NoPollPolicyInConcurrencySuite(t *testing.T) {
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
		if !strings.HasPrefix(name, "attempt_gate_") {
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
			if !ok {
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
					t.Fatalf("attempt_gate suite must not use time.%s for sync at %s:%d (use barriers/channels; context timeout only as post-barrier deadlock guard; past time.Time for expired deadline is OK)",
						sel.Sel.Name, filepath.Base(pos.Filename), pos.Line)
				}
			case "runtime":
				switch sel.Sel.Name {
				case "Gosched":
					t.Fatalf("attempt_gate suite must not use runtime.%s scheduler steering at %s:%d",
						sel.Sel.Name, filepath.Base(pos.Filename), pos.Line)
				}
			}
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("expected attempt_gate_*_test.go files to scan")
	}

	for _, path := range []string{"attempt_gate.go", "coordinator.go"} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		var wait *ast.FuncDecl
		switch path {
		case "attempt_gate.go":
			wait = findMethod(file, "attemptGate", "WaitForIdle")
		case "coordinator.go":
			wait = findMethod(file, "Coordinator", "WaitForIdle")
		}
		if wait == nil || wait.Body == nil {
			t.Fatalf("%s WaitForIdle missing", path)
		}
		if funcContainsPollingTimer(wait.Body) {
			t.Fatalf("%s WaitForIdle must not contain polling/periodic timers", path)
		}
	}
}
