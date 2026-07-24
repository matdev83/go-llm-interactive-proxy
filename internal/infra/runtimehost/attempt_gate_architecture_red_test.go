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
// and requires every row to declare characterization, AST RED, or Task 6.2
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

	// Compile-time vocabulary: admission + finish outcomes cover the narrow transaction.
	_ = []attemptAdmissionKindContract{
		admissionAdmittedContract,
		admissionBusyAPIContract,
		admissionPendingHUPContract,
		admissionCoalescedHUPContract,
		admissionRejectedShutdownContract,
	}
	_ = []attemptFinishKindContract{
		finishReleasedIdleContract,
		finishFollowUpClaimedContract,
		finishAlreadyCompletedContract,
	}
	_ = attemptAdmissionOutcomeContract{}
	_ = attemptFinishOutcomeContract{}
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
			// Contract vocabulary / AST inventory anchors (non-Test*).
			switch part {
			case "attemptFinishOutcomeContract",
				"attemptAdmissionOutcomeContract",
				"attemptGateContract",
				"attemptLeaseContract":
				// named in contract file; compile-checked above
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

// TestAttemptGate_ArchitectureREDInventory is the Task 6.1 AST characterization:
// Coordinator still owns gate responsibilities; production AttemptGate is absent;
// WaitForIdle still polls; Reload can expose busy before armAttempt; pending
// follow-up claim remains inlined in Reload; concurrent duplicate release is unsafe.
// The suite is GREEN by recording these as explicit RED baseline facts Task 6.2 flips.
func TestAttemptGate_ArchitectureREDInventory(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	coordPath := "coordinator.go"
	src, err := os.ReadFile(coordPath)
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(fset, coordPath, src, 0)
	if err != nil {
		t.Fatal(err)
	}

	coord := findTypeSpec(file, "Coordinator")
	if coord == nil {
		t.Fatal("RED inventory: Coordinator type missing")
	}
	st, ok := coord.Type.(*ast.StructType)
	if !ok || st.Fields == nil {
		t.Fatal("RED inventory: Coordinator is not a struct")
	}
	fields := map[string]bool{}
	for _, f := range st.Fields.List {
		for _, name := range f.Names {
			fields[name.Name] = true
		}
	}
	owned := []string{
		"busy",
		"pendingSignal",
		"coalesced",
		"attemptCancel",
		"attemptDone",
		"attemptOnce",
		"shutdown",
	}
	for _, name := range owned {
		if !fields[name] {
			t.Fatalf("RED inventory: Coordinator must still own field %q until Task 6.2", name)
		}
	}
	t.Log("RED: Coordinator still owns busy/pendingSignal/coalesced/attemptCancel/attemptDone/attemptOnce/shutdown")

	for _, name := range []string{"BeginShutdown", "WaitForIdle", "armAttempt", "releaseAttempt", "Reload"} {
		if findMethod(file, "Coordinator", name) == nil {
			t.Fatalf("RED inventory: missing Coordinator.%s", name)
		}
	}
	t.Log("RED: busy admission, pending SIGHUP, coalesce, cancel, shutdown, completion channel, WaitForIdle remain on Coordinator")

	if productionAttemptGateOwnerExists(t) {
		t.Fatal("Task 6.1 RED baseline expects no production AttemptGate owner yet; Task 6.2 must introduce it")
	}
	t.Log("RED: no production AttemptGate type/owner exists yet")

	wait := findMethod(file, "Coordinator", "WaitForIdle")
	if wait == nil || wait.Body == nil {
		t.Fatal("WaitForIdle missing body")
	}
	if !funcContainsPollingTimer(wait.Body) {
		t.Fatal("Task 6.1 RED baseline expects WaitForIdle polling (time.After/ticker/sleep); Task 6.2 must delete it")
	}
	t.Log("RED: Coordinator.WaitForIdle contains polling/periodic timer")

	reload := findMethod(file, "Coordinator", "Reload")
	if reload == nil || reload.Body == nil {
		t.Fatal("Reload missing body")
	}
	busyPos, armPos, ok := busyAssignmentBeforeArmAttempt(reload.Body)
	if !ok {
		t.Fatal("Task 6.1 RED baseline expects Reload to assign busy before armAttempt")
	}
	if !busyPos.IsValid() || !armPos.IsValid() || busyPos >= armPos {
		t.Fatalf("busy-before-armed order invalid: busy=%v arm=%v", busyPos, armPos)
	}
	t.Logf("RED: Reload sets busy at %s before armAttempt at %s", fset.Position(busyPos), fset.Position(armPos))

	if !reloadClaimsPendingFollowUpInline(reload.Body) {
		t.Fatal("Task 6.1 RED baseline expects Reload to inline pending-HUP claim + coalesced reset; Task 6.2 Complete() must own that transition")
	}
	t.Log("RED: pending follow-up claim/coalesced reset still inlined in Coordinator.Reload (no Complete outcome API yet)")

	release := findMethod(file, "Coordinator", "releaseAttempt")
	if release == nil || release.Body == nil {
		t.Fatal("releaseAttempt missing body")
	}
	if !releaseAttemptHasRacyElseClose(release.Body) {
		t.Fatal("Task 6.1 RED baseline expects releaseAttempt else-branch unprotected close; Task 6.2 Finish must be race-safe")
	}
	t.Log("RED: releaseAttempt else branch can close without Once under concurrent duplicate Complete — source-level only until Task 6.2")
}

// TestAttemptGate_CallerCancelDetachmentEvidence locks caller-cancel detachment:
// Reload uses context.WithoutCancel, and the focused behavioral proof test exists.
func TestAttemptGate_CallerCancelDetachmentEvidence(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	src, err := os.ReadFile("coordinator.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(fset, "coordinator.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	reload := findMethod(file, "Coordinator", "Reload")
	if reload == nil || reload.Body == nil {
		t.Fatal("Reload missing")
	}
	if !funcContainsWithoutCancel(reload.Body) {
		t.Fatal("expected Coordinator.Reload to call context.WithoutCancel for host-owned attempt detachment (req 6.10)")
	}
	t.Log("characterization/AST: Reload uses context.WithoutCancel(ctx) so trigger-caller cancel does not own the admitted attempt")

	tests := collectAttemptGatePackageTestNames(t)
	if !tests["TestCoordinator_HostTimeoutIndependentOfClientCancel"] {
		t.Fatal("expected existing TestCoordinator_HostTimeoutIndependentOfClientCancel as behavioral detachment proof")
	}
}

func productionAttemptGateOwnerExists(t *testing.T) bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
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
		if findTypeSpec(file, "AttemptGate") != nil {
			return true
		}
		if findTypeSpec(file, "attemptGate") != nil {
			return true
		}
	}
	return false
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

// releaseAttemptHasRacyElseClose detects the current unprotected close(done)
// fallback used when attemptOnce is already cleared — unsafe under concurrent
// duplicate Complete and an explicit Task 6.2 contract gap.
func releaseAttemptHasRacyElseClose(body *ast.BlockStmt) bool {
	foundClose := false
	ast.Inspect(body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok || ifStmt.Else == nil {
			return true
		}
		elseBlock, ok := ifStmt.Else.(*ast.BlockStmt)
		if !ok {
			return true
		}
		ast.Inspect(elseBlock, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if ok && ident.Name == "close" {
				foundClose = true
				return false
			}
			return true
		})
		return true
	})
	return foundClose
}

// busyAssignmentBeforeArmAttempt reports whether Reload assigns c.busy=true
// at a source position strictly before calling armAttempt.
func busyAssignmentBeforeArmAttempt(body *ast.BlockStmt) (busyPos, armPos token.Pos, ok bool) {
	var busy token.Pos
	var arm token.Pos
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				sel, isSel := lhs.(*ast.SelectorExpr)
				if !isSel || sel.Sel == nil || sel.Sel.Name != "busy" {
					continue
				}
				if i < len(node.Rhs) {
					if lit, isLit := node.Rhs[i].(*ast.Ident); isLit && lit.Name == "true" {
						if !busy.IsValid() || node.Pos() < busy {
							busy = node.Pos()
						}
					}
				}
			}
		case *ast.CallExpr:
			switch fun := node.Fun.(type) {
			case *ast.SelectorExpr:
				if fun.Sel != nil && fun.Sel.Name == "armAttempt" {
					if !arm.IsValid() || node.Pos() < arm {
						arm = node.Pos()
					}
				}
			case *ast.Ident:
				if fun.Name == "armAttempt" {
					if !arm.IsValid() || node.Pos() < arm {
						arm = node.Pos()
					}
				}
			}
		}
		return true
	})
	if busy.IsValid() && arm.IsValid() && busy < arm {
		return busy, arm, true
	}
	return 0, 0, false
}

// reloadClaimsPendingFollowUpInline detects the current Reload loop that clears
// pendingSignal and resets coalesced while still owning the active attempt slot
// (no Complete() follow-up outcome API yet — Task 6.2 contract gap).
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
// scheduler steering in Task 6.1 attempt_gate_* tests. Production WaitForIdle
// polling is inventoried as RED evidence (does not fail this suite). A past
// time.Time used only to build an already-expired context deadline is allowed.
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
					t.Fatalf("Task 6.1 suite must not use time.%s for sync at %s:%d (use barriers/channels; context timeout only as post-barrier deadlock guard; past time.Time for expired deadline is OK)",
						sel.Sel.Name, filepath.Base(pos.Filename), pos.Line)
				}
			case "runtime":
				switch sel.Sel.Name {
				case "Gosched":
					t.Fatalf("Task 6.1 suite must not use runtime.%s scheduler steering at %s:%d",
						sel.Sel.Name, filepath.Base(pos.Filename), pos.Line)
				}
			}
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("expected attempt_gate_*_test.go files to scan")
	}

	// Production RED inventory (pass while polling remains).
	coordSrc, err := os.ReadFile("coordinator.go")
	if err != nil {
		t.Fatal(err)
	}
	coordFile, err := parser.ParseFile(fset, "coordinator.go", coordSrc, 0)
	if err != nil {
		t.Fatal(err)
	}
	wait := findMethod(coordFile, "Coordinator", "WaitForIdle")
	if wait == nil || !funcContainsPollingTimer(wait.Body) {
		t.Fatal("production WaitForIdle polling RED evidence missing; if Task 6.2 already removed it, flip this inventory")
	}
	t.Log("RED inventory: production WaitForIdle still polls; Task 6.2 must delete polling and activate zero-poll production gate")
}
