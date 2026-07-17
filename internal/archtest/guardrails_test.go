package archtest

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Budgets are non-test line counts (approximate architectural mass).
// Raise only when intentionally growing a layer; see docs/architecture-guardrails.md.
var lineBudgets = []struct {
	dir string
	max int
}{
	// internal/core was raised from 32000 to 33000 for the admission-policy-decision-core
	// spec, which adds the shared policy decision vocabulary, legality/evidence/timeout
	// infrastructure, and stage-runner error taxonomy integration to the core layer.
	// Raised from 33000 to 33500 for the control-plane-persistence-query-event-ledger
	// spec Phase 1, which adds the core control-plane validation, status state, and
	// store/clock/identity ports under internal/core/controlplane.
	// Raised from 33500 to 35000 for the control-plane-persistence-query-event-ledger
	// spec Phase 3, which adds the core scope flattener, event normalizer, recorder
	// service, query service, and retention controller under internal/core/controlplane.
	// Raised from 35000 to 38000 for the usage-quota-rate-budget-authority spec,
	// which adds the usageauthority bounded context under internal/core.
	// Raised from 38000 to 38500 to accommodate the full implementation of the
	// usage-quota-rate-budget-authority spec, including runtime admission/settlement
	// integration and the usageauthority bounded context under internal/core.
	// Raised from 38500 to 38800 to accommodate the authority reservation leak-fix
	// additions to internal/core/runtime (recv-loop/handler/interleaved/parallel
	// leak-path hardening and the executor recv-handler settlement chokepoint).
	// Raised from 38800 to 38900 to accommodate the usage-quota-rate-budget-authority
	// spec A1/A2 domain & contract foundation: the Credential authority dimension
	// (Dimensions/DimensionsMatcher/Key/Matches, config matcher, store scope
	// snapshot + ScopeDimensionsMatch, runtime scope projection with safe-label
	// filtering), the authority-unavailable rule outcome for authoritative-only
	// rules under estimated evidence, and the spend-cap clamp EffectiveMax/
	// RequestedMax fields; plus the in-progress multi-rule admission/settlement
	// and recv-loop leak-fix work on the same branch.
	// Raised from 39700 to 40000 to accommodate the medium-findings follow-up:
	// lifecycle-aware settlement/release evidence projection, the accompanying
	// app/runtime test assertions, and the related spec sync cleanup on this
	// branch.
	// Raised from 40000 to 40500 for the remediation pass: app-owned atomic
	// reservation descriptors, per-unit settlement, durable lifecycle evidence,
	// and the accompanying boundary tests.
	// Raised from 40500 to 41000 for the decision-authority remediation: bounded
	// evaluation, explicit usage provenance, compensation, reconciliation, and
	// per-rule unreserved usage facts across runtime and durable stores.
	// Raised from 41000 to 41200 for live spend-cap capacity refinement in the
	// application layer and durable configuration reconciliation in the authority
	// store, including their regression coverage and orchestration helpers.
	// Raised from 41200 to 41250 for rule-attributed multi-rule reservation
	// recovery and independently bounded detached authority cleanup contexts.
	// Raised from 41250 to 41300 for scope-isolated spend-cap lookup and a fresh
	// fallback-release cleanup deadline after failed settlement.
	// Raised from 41300 to 41500 for the coordinated authority stabilization:
	// independent token/cost authority state, typed durable capacity replay,
	// targeted PostgreSQL proof seams, and the differential adapter coverage.
	// Raised from 41500 to 41700 for review remediation: CostPresent monetary
	// presence, unenforceable spend-cap candidate exclusion, multi-scope
	// settlement aggregation, and zero-remaining output budget denial.
	// Raised from 41700 to 42200 to accommodate internal/core/codexcatalog, the
	// auto-discovered Codex model catalog (parser, fallback snapshot, discovery,
	// binary resolver) shared by the openai-codex and codex app-server connectors.
	// Raised from 42200 to 42500 for dual-plane economics Phase 2 correctness:
	// token-total inclusion/presence, checked money arithmetic with optional
	// rate presence, and unknown-output preflight policy resolution.
	// Raised from 42500 to 43500 for Phase 4 metering checkpoints (capture,
	// widening, egress fact drafts) plus runtime hook wiring.
	// Raised from 43500 to 44000 for Phase 5 aggregate + reconcile packages.
	// Raised from 44000 to 45000 for Phase 6 authority coordinators + adapters.
	// Raised from 45000 to 45500 for Phase 7 usage-authority dual-plane kernel.
	// Raised from 45500 to 47000 for Phase 8 concurrencyauthority domain/app + runtime lease ownership.
	// Raised from 47000 to 47500 for Phase 9 snapshotgen RuntimeGeneration publisher.
	// Raised from 47500 to 48000 for Phase 11.2 control-plane query bounds validation.
	// Raised from 48000 to 48100 for Phase 12 rem: settle/release/query stage metrics (16.5).
	// Raised from 48100 to 49400 for injected-rater OutputLimitQuoter clamp path
	// plus dual-plane remediations on this branch (no silent catalog fallback).
	// Raised from 49400 to 49600 for FE/BE deferred ingress counting + BE freeze
	// before attempt authorization (reqs 2.1, 2.2, 4.1, 5.1).
	// Raised from 49600 to 49750 for explicit PostgreSQL connection/schema modes
	// and validation; this is typed configuration, not runtime control-flow growth.
	// Raised from 49750 to 49950 for live model-registry inventory/runtime growth
	// (issue #146): BackendModel discovery diagnostics and refresh seams in
	// internal/core/modelregistry. The OpenAI-compatible GET /v1/models HTTP DTO
	// stays in internal/stdhttp (not core).
	// Raised from 49950 to 50050 for issue #146 access-mode gate comments on
	// CodexModelCatalogConfig (single_user + enabled-consumer discovery).
	// Raised from 50050 to 50300 for issue #146/#149 model-registry perf and
	// inventory completion: publish-time OpenAI list JSON, content fingerprint,
	// parallel inventory fetch, and refresh overlap guard in modelregistry.
	// Raised from 50300 to 50700 for issue #147 identity foundation: pure
	// identity policy model, defaults/validation/merge, and AcceptClientUserAgent.
	// Raised for issue #147 OpenRouter attribution: AcceptClientAppURL/AppTitle.
	// Raised from 50700 to 50900 for issue #147 gap closure: SpecBundleIdentityScenarios.
	// Raised from 50900 to 51300 for protocol-neutral internal/core/jsonshape
	// (encoding/json.Decoder.Token size/shape preflight shared by frontend
	// jsonguard). Measured post-change non-test total is 51185; cap keeps ~115
	// lines of headroom.
	// Raised from 51300 to 53900 for issue #152 tool-call repair (toolcallrepair +
	// assembler/finalization). Measured post-change non-test total is 53816; cap
	// keeps ~80 lines of headroom.
	{"internal/core", 53900},
	{"internal/pluginreg", 4500},
	{"internal/stdhttp", 3500},
	// Raised from 4650 to 4800 for dynamic snapshot SnapshotController refresh
	// lifecycle (requirements 11.3, 11.6, 11.7).
	// Raised from 4800 to 5200 for build-local PostgreSQL pool ownership,
	// centralized schema lifecycle, and the associated runtime readiness and
	// integration seams. The current 4929-line package remains below this cap
	// with meaningful headroom for follow-up changes; keep new growth in focused
	// files and do not use this budget to excuse Build orchestrator re-bloat.
	{"internal/infra/runtimebundle", 5200},
}

func TestLineComplexityBudgets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, b := range lineBudgets {
		t.Run(b.dir, func(t *testing.T) {
			t.Parallel()
			n, err := countNonTestGoLines(filepath.Join(root, b.dir))
			if err != nil {
				t.Fatal(err)
			}
			if n > b.max {
				t.Fatalf("%s: %d non-test lines exceeds budget %d (see docs/architecture-guardrails.md)", b.dir, n, b.max)
			}
		})
	}
}

// criticalFileBudgets locks single-file gravity wells from silently re-bloating.
// These complement the tree-level lineBudgets above. Budgets are non-test line counts
// measured with the same bufio.Scanner methodology as countNonTestGoLines.
// Rationale and values are maintained in CriticalFileBudgets so make arch-report
// reports the same hotspot list.
var criticalFileBudgets = CriticalFileBudgets

func TestCriticalFileLineBudgets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, b := range criticalFileBudgets {
		t.Run(strings.ReplaceAll(b.Path, "/", "_"), func(t *testing.T) {
			t.Parallel()
			n, err := countFileLines(filepath.Join(root, b.Path))
			if err != nil {
				t.Fatalf("%s: %v", b.Path, err)
			}
			if n > b.Max {
				t.Fatalf("%s: %d non-test lines exceeds critical-file budget %d (see docs/architecture-guardrails.md)", b.Path, n, b.Max)
			}
			t.Logf("%s: %d/%d lines", b.Path, n, b.Max)
		})
	}
}

func TestStandardBundlePackagesHaveNoInitFunctions(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "pluginreg"),
		filepath.Join(root, "internal", "standardplugins"),
		filepath.Join(root, "cmd", "lipstd"),
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			t.Parallel()
			err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					return nil
				}
				if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
					return nil
				}
				if hasInitFunc(path) {
					t.Fatalf("forbid init() in standard bundle path (explicit InstallStandardBundleOn/validation from composition root): %s", path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTestsMustNotRegisterStandardBundleInInit(t *testing.T) {
	t.Parallel()
	initDecl := "func init" + "("
	regStd := "RegisterStandard" + "Bundle()"
	root := repoRoot(t)
	var bad []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if base == "vendor" || base == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s := string(b)
		if strings.Contains(s, initDecl) && strings.Contains(s, regStd) {
			bad = append(bad, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("forbid init-time standard bundle registration in tests; install factories on a fresh registry from tests/helpers explicitly:\n%s", strings.Join(bad, "\n"))
	}
}

func TestRuntimebundleDoesNotSelectPluginregDefault(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "infra", "runtimebundle")
	var bad []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if refsPluginregDefaultSelector(t, path, src) {
			bad = append(bad, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("runtimebundle must not reference pluginreg.Default (pass *pluginreg.Registry via BuildOptions); offending files:\n%s", strings.Join(bad, "\n"))
	}
}

func TestCompositionLayersDoNotRegisterStandardBundle(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "infra", "runtimebundle"),
		filepath.Join(root, "internal", "stdhttp"),
	}
	// bootstrap_plan.go is the single allowed call site for InstallStandardBundleOn
	// inside the runtimebundle composition root (BuildBootstrap startup helper).
	// All other runtimebundle and stdhttp files must not install the standard bundle.
	allowedFile := filepath.Join(root, "internal", "infra", "runtimebundle", "bootstrap_plan.go")
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			t.Parallel()
			var bad []string
			err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					return nil
				}
				if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
					return nil
				}
				if path == allowedFile {
					return nil // bootstrap_plan.go is the composition-root startup path
				}
				src, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if callsStandardBundleInstall(t, path, src) {
					bad = append(bad, path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(bad) != 0 {
				t.Fatalf("%s: forbid standard bundle installation in composition layer (only bootstrap_plan.go may install; install in cmd/lipstd or tests, pass registry in): %s", dir, strings.Join(bad, "\n"))
			}
		})
	}
}

func TestWiringRootsHaveNoPackageLevelPluginRegistryOrSyncOnce(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "infra", "runtimebundle"),
		filepath.Join(root, "internal", "stdhttp"),
		filepath.Join(root, "cmd", "lipstd"),
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			t.Parallel()
			var badReg, badOnce []string
			err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					return nil
				}
				if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
					return nil
				}
				src, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				pr, once := packageLevelRegistryVarOrSyncOnce(t, path, src)
				if pr {
					badReg = append(badReg, path)
				}
				if once {
					badOnce = append(badOnce, path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(badReg) != 0 {
				t.Fatalf("%s: forbid package-level *pluginreg.Registry / NewRegistry vars (registry owned by composition root, threaded as parameters): %s", dir, strings.Join(badReg, "\n"))
			}
			if len(badOnce) != 0 {
				t.Fatalf("%s: forbid package-level sync.Once (no lazy standard-bundle or registry singletons in wiring): %s", dir, strings.Join(badOnce, "\n"))
			}
		})
	}
}

func TestCompositionRootDoesNotPairSyncOnceWithStandardBundleInstall(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "cmd", "lipstd")
	var bad []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if fileReferencesSyncOnce(t, path, src) && callsStandardBundleInstall(t, path, src) {
			bad = append(bad, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("cmd/lipstd: forbid sync.Once + standard bundle install in the same file (no lazy registration); offending files:\n%s", strings.Join(bad, "\n"))
	}
}

func refsPluginregDefaultSelector(t *testing.T, filename string, src []byte) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		xid, ok := sel.X.(*ast.Ident)
		if !ok || xid.Name != "pluginreg" {
			return true
		}
		if sel.Sel == nil || sel.Sel.Name != "Default" {
			return true
		}
		found = true
		return false
	})
	return found
}

func callsStandardBundleInstall(t *testing.T, filename string, src []byte) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := callName(call.Fun)
		if name == "InstallStandardBundleOn" {
			found = true
			return false
		}
		return true
	})
	return found
}

func callName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if f.Sel != nil {
			return f.Sel.Name
		}
	}
	return ""
}

func packageLevelRegistryVarOrSyncOnce(t *testing.T, filename string, src []byte) (badRegVar bool, badPkgOnce bool) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if vs.Type != nil && isStarPluginregRegistry(vs.Type) {
				badRegVar = true
			}
			for _, v := range vs.Values {
				if isPluginregNewRegistryCall(v) {
					badRegVar = true
				}
			}
			if vs.Type != nil && isSyncOnceType(vs.Type) {
				badPkgOnce = true
			}
		}
	}
	return badRegVar, badPkgOnce
}

func isStarPluginregRegistry(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	xid, ok := sel.X.(*ast.Ident)
	if !ok || xid.Name != "pluginreg" {
		return false
	}
	return sel.Sel != nil && sel.Sel.Name == "Registry"
}

func isPluginregNewRegistryCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	if callName(call.Fun) != "NewRegistry" {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	xid, ok := sel.X.(*ast.Ident)
	return ok && xid.Name == "pluginreg"
}

func isSyncOnceType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	xid, ok := sel.X.(*ast.Ident)
	if !ok || xid.Name != "sync" {
		return false
	}
	return sel.Sel != nil && sel.Sel.Name == "Once"
}

func fileReferencesSyncOnce(t *testing.T, filename string, src []byte) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		xid, ok := sel.X.(*ast.Ident)
		if !ok || xid.Name != "sync" {
			return true
		}
		if sel.Sel != nil && sel.Sel.Name == "Once" {
			found = true
			return false
		}
		return true
	})
	return found
}

func countNonTestGoLines(dir string) (int, error) {
	var total int
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		n, err := countFileLines(path)
		if err != nil {
			return err
		}
		total += n
		return nil
	})
	return total, err
}

// countFileLines returns the number of lines in a single file. Used for per-file
// critical-file budgets and the architecture report.
func countFileLines(path string) (n int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	sc := bufio.NewScanner(f)
	// Raise the per-line buffer so long generated/fixture-style lines are still
	// counted; budgets are approximate architectural mass, not exact SLOC.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		n++
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return n, nil
}

func hasInitFunc(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	s := string(b)
	// Naive but sufficient: registration must not hide in init().
	return strings.Contains(s, "func init(")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for range 12 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find go.mod above", wd)
	return ""
}

// referencesIdent reports whether any *ast.Ident in the file has the given name. It
// catches both field declarations (Field.Names) and selector/operand references, so a
// guardrail can forbid an identifier from a whole layer without regard to how it is used.
func referencesIdent(t *testing.T, filename string, src []byte, name string) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if id.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

// TestPolicyDiagnosticsEnabledNotReferencedFromFrontendOrStdhttp locks requirement 7.4:
// the privileged-diagnostics flag must be settable only from the composition root
// (runtimebundle.BuildOptions -> runtime.Executor), never from a frontend HTTP adapter
// or stdhttp request path. A client request must not be able to enable privileged
// policy decision evidence.
func TestPolicyDiagnosticsEnabledNotReferencedFromFrontendOrStdhttp(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "plugins", "frontends"),
		filepath.Join(root, "internal", "stdhttp"),
	}
	var bad []string
	for _, dir := range dirs {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if referencesIdent(t, path, src, "PolicyDiagnosticsEnabled") {
				bad = append(bad, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(bad) != 0 {
		t.Fatalf("PolicyDiagnosticsEnabled must not be referenced from frontend/stdhttp request paths (composition-root-only, requirement 7.4):\n%s", strings.Join(bad, "\n"))
	}
}

// TestFailureModeAndTimeoutBudgetNotClientSourced locks requirements 6.2 and 6.3: policy
// failure behavior and evaluation timeout budgets are sourced only from plugin
// interface methods and the frozen RequestRuntimeSnapshot (composition root), never
// from client request input decoded in a frontend adapter. An external client must not
// be able to set failure modes or timeout budgets.
func TestFailureModeAndTimeoutBudgetNotClientSourced(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "plugins", "frontends")
	forbidden := []string{"FailOpen", "FailClosed", "FailureMode", "TimeoutBudget", "TimeoutFor"}
	var bad []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, name := range forbidden {
			if referencesIdent(t, path, src, name) {
				bad = append(bad, path+" ("+name+")")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("frontend adapters must not reference failure-mode/timeout-budget identifiers (plugin/composition-root-only, requirements 6.2/6.3):\n%s", strings.Join(bad, "\n"))
	}
}
