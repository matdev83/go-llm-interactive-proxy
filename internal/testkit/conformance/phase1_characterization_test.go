package conformance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuation"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	stdhttpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	sdkcontinuation "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
	"gopkg.in/yaml.v3"
)

type cartesianFileDetail struct {
	FilePath            string   `json:"file_path"`
	Functions           []string `json:"functions"`
	NonGeneratedGoLines int      `json:"non_generated_go_lines"`
}

type baselineInventory struct {
	BaselineSHA            string                `json:"baseline_sha"`
	BundledFrontendIDs     []string              `json:"bundled_frontend_ids"`
	BundledBackendIDs      []string              `json:"bundled_backend_ids"`
	MatrixCellCount        int                   `json:"matrix_cell_count"`
	RequiredFeatureCount   int                   `json:"required_feature_count"`
	EssentialBackendKinds  []string              `json:"essential_backend_kinds"`
	CompatibleBackendKinds []string              `json:"compatible_backend_kinds"`
	BackendPluginVersions  []string              `json:"backend_plugin_versions"`
	TotalCartesianGoLines  int                   `json:"total_cartesian_go_lines"`
	CartesianFiles         []cartesianFileDetail `json:"cartesian_files"`
}

// TestPhase1_Task11_FreezeBaselines verifies all baseline locks, registration structures,
// route claims, diagnostic JSON shapes, ABI versions, continuation types, and machine-readable inventory.
func TestPhase1_Task11_FreezeBaselines(t *testing.T) {
	t.Parallel()

	// 1. Verify Bundled Frontend & Backend IDs and Cartesian Cell Count
	fe := BundledFrontendIDs()
	be := BundledBackendIDs()
	cells := AllCells()

	if len(fe) != 5 {
		t.Fatalf("expected 5 bundled frontends, got %d (%v)", len(fe), fe)
	}
	if len(be) != 9 {
		t.Fatalf("expected 9 bundled backends, got %d (%v)", len(be), be)
	}
	if len(cells) != 45 {
		t.Fatalf("expected 45 Cartesian cells, got %d", len(cells))
	}

	// Verify first cell frontend and backend
	if cells[0].Frontend != "openai-responses" || cells[0].Backend != "openai-responses" {
		t.Fatalf("expected first cell openai-responses:openai-responses, got %s:%s", cells[0].Frontend, cells[0].Backend)
	}

	// 2. Verify 17 Required Features
	reqFeatures := OpenResponsesFrontendRowRequiredFeatures()
	if len(reqFeatures) != 17 {
		t.Fatalf("expected 17 required features in OpenResponses frontend row, got %d (%v)", len(reqFeatures), reqFeatures)
	}
	expectedFeatureIDs := []FeatureID{
		FeatureJSONText, FeatureSSEText, FeatureInstructionsRoles, FeatureHistory,
		FeatureTools, FeatureMultimodal, FeatureAssistantMedia, FeatureUsageErrors,
		FeatureReasoningReplay, FeatureAssistantPhase, FeatureItemReferences, FeatureContinuation,
		FeatureCompaction, FeatureExtensions, FeatureCancellation, FeatureFailover,
		FeatureNoRetryVisibleOutput,
	}
	for _, expectedID := range expectedFeatureIDs {
		if !slices.Contains(reqFeatures, expectedID) {
			t.Fatalf("required features list missing expected feature ID %q", expectedID)
		}
	}

	// 3. Verify Standard/Essential/Compatible backend lists
	essential := standardplugins.EssentialBackendKinds()
	if len(essential) != 10 {
		t.Fatalf("expected 10 essential backend kinds, got %d (%v)", len(essential), essential)
	}

	compatible := standardplugins.CompatibleBackendKinds()
	if len(compatible) != 4 {
		t.Fatalf("expected 4 compatible backend kinds, got %d (%v)", len(compatible), compatible)
	}

	// 4. Verify Normalized Route Claims from stdhttp contract for ALL bundled frontends
	openresponsesClaims, err := stdhttpcontract.OpenResponsesDefaultClaims("openresponses")
	if err != nil {
		t.Fatalf("unexpected error getting openresponses default claims: %v", err)
	}
	openaiResponsesClaims, err := stdhttpcontract.OpenAIResponsesDefaultClaims("openai-responses")
	if err != nil {
		t.Fatalf("unexpected error getting openai-responses default claims: %v", err)
	}
	openaiLegacyClaim, err := stdhttpcontract.RouteClaim{OwnerID: "openai-legacy", Method: "POST", Path: "/v1/chat/completions", Kind: "openai_chat_completions"}.NormalizedClaim()
	if err != nil {
		t.Fatalf("unexpected error getting openai-legacy claim: %v", err)
	}
	anthropicClaim, err := stdhttpcontract.RouteClaim{OwnerID: "anthropic", Method: "POST", Path: "/v1/messages", Kind: "anthropic_messages"}.NormalizedClaim()
	if err != nil {
		t.Fatalf("unexpected error getting anthropic claim: %v", err)
	}
	// Keep this expected set explicit and normalized: route ownership is a
	// contribution contract, not a count/owner smoke check.
	expectedClaims := map[string][]stdhttpcontract.RouteClaim{
		"openai-responses": openaiResponsesClaims,
		"openresponses":    openresponsesClaims,
		"openai-legacy":    {openaiLegacyClaim},
		"anthropic":        {anthropicClaim},
		"gemini": {
			{OwnerID: "gemini", Method: "POST", Path: "/v1beta/", Kind: "gemini_generate"},
			{OwnerID: "gemini", Method: "POST", Path: "/v1beta1/", Kind: "gemini_generate"},
		},
	}
	for owner, claims := range expectedClaims {
		for i := range claims {
			normalized, err := claims[i].NormalizedClaim()
			if err != nil {
				t.Fatalf("expected route %q claim %d is invalid: %v", owner, i, err)
			}
			claims[i] = normalized
		}
	}
	providers := standardplugins.StandardFrontendRouteClaims()
	claimKey := func(c stdhttpcontract.RouteClaim) string {
		return c.OwnerID + "|" + c.Method + "|" + c.Path + "|" + string(c.Kind)
	}
	claimMultiset := func(claims []stdhttpcontract.RouteClaim) map[string]int {
		counts := make(map[string]int, len(claims))
		for _, claim := range claims {
			normalized, err := claim.NormalizedClaim()
			if err != nil {
				t.Fatalf("invalid actual route claim %+v: %v", claim, err)
			}
			counts[claimKey(normalized)]++
		}
		return counts
	}
	for _, owner := range fe {
		provider, ok := providers[owner]
		if !ok {
			t.Fatalf("missing real bundled frontend route contribution for %q", owner)
		}
		actual, err := provider(owner, yaml.Node{})
		if err != nil {
			t.Fatalf("route contribution for %q: %v", owner, err)
		}
		got, want := claimMultiset(actual), claimMultiset(expectedClaims[owner])
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("route contribution for %q differs from exact claim multiset: got=%v want=%v", owner, got, want)
		}
	}

	// Equal cardinality must not hide an owner/path/method/kind substitution,
	// removal, addition, or duplicate mutation. Multiset comparison preserves
	// duplicate claims instead of collapsing them into a map-set.
	base := append([]stdhttpcontract.RouteClaim(nil), expectedClaims["gemini"]...)
	mutations := map[string]func([]stdhttpcontract.RouteClaim) []stdhttpcontract.RouteClaim{
		"owner": func(in []stdhttpcontract.RouteClaim) []stdhttpcontract.RouteClaim {
			in[0].OwnerID = "substituted-owner"
			return in
		},
		"path": func(in []stdhttpcontract.RouteClaim) []stdhttpcontract.RouteClaim {
			in[0].Path = "/v1beta2/"
			return in
		},
		"method": func(in []stdhttpcontract.RouteClaim) []stdhttpcontract.RouteClaim {
			in[0].Method = "GET"
			return in
		},
		"kind": func(in []stdhttpcontract.RouteClaim) []stdhttpcontract.RouteClaim {
			in[0].Kind = "anthropic_messages"
			return in
		},
		"removal": func(in []stdhttpcontract.RouteClaim) []stdhttpcontract.RouteClaim {
			return append(in[:0], in[1:]...)
		},
		"addition": func(in []stdhttpcontract.RouteClaim) []stdhttpcontract.RouteClaim {
			in[0].Path = "/v1beta2/"
			return in
		},
		"extra-distinct-claim": func(in []stdhttpcontract.RouteClaim) []stdhttpcontract.RouteClaim {
			return append(in, stdhttpcontract.RouteClaim{OwnerID: "gemini", Method: "POST", Path: "/v1beta2/models", Kind: "gemini_generate"})
		},
		"duplicate": func(in []stdhttpcontract.RouteClaim) []stdhttpcontract.RouteClaim {
			in[1] = in[0]
			return in
		},
	}
	for name, mutate := range mutations {
		mutated := mutate(append([]stdhttpcontract.RouteClaim(nil), base...))
		if reflect.DeepEqual(claimMultiset(mutated), claimMultiset(base)) {
			t.Fatalf("route mutation %s was not rejected by exact claim multiset", name)
		}
	}

	// 5. Exercise the actual HTTP diagnostic handler and lock its JSON shape.
	cfg := &config.Config{Plugins: config.PluginsConfig{
		Frontends: []config.PluginConfig{{ID: "fe-1", Kind: "openai-responses", Enabled: true}},
		Backends:  []config.PluginConfig{{ID: "be-1", Kind: "openai-responses", Enabled: true}},
	}}
	handler, err := diag.InventoryHandler(cfg, &diag.InventoryExtras{})
	if err != nil {
		t.Fatalf("create diagnostic handler: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/diagnostics/inventory", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("diagnostic handler status: %d", res.Code)
	}
	snapJSON := res.Body.Bytes()

	var jsonMap map[string]any
	if err := json.Unmarshal(snapJSON, &jsonMap); err != nil {
		t.Fatalf("unmarshal diag snapshot into generic map: %v", err)
	}
	for _, key := range []string{"frontends", "backends"} {
		v, ok := jsonMap[key]
		if !ok {
			t.Fatalf("diag JSON shape missing key %q: %s", key, string(snapJSON))
		}
		arr, ok := v.([]any)
		if !ok || len(arr) == 0 {
			t.Fatalf("diag JSON shape key %q must be non-empty array", key)
		}
		elem, ok := arr[0].(map[string]any)
		if !ok {
			t.Fatalf("diag JSON shape array element for %q must be object", key)
		}
		if key == "frontends" && (elem["id"] != "fe-1" || elem["factory_kind"] != "openai-responses" || elem["enabled"] != true) {
			t.Fatalf("frontends diagnostic field-level representative values mismatch: %+v", elem)
		}
		if key == "backends" && (elem["id"] != "be-1" || elem["factory_kind"] != "openai-responses" || elem["enabled"] != true) {
			t.Fatalf("backends diagnostic field-level representative values mismatch: %+v", elem)
		}

	}

	// 6. Verify Baseline Machine-Readable Inventory File Presence
	rawInv, err := os.ReadFile(filepath.Join("testdata", "baseline_cartesian_inventory.json"))
	if err != nil {
		t.Fatalf("read baseline inventory file: %v", err)
	}

	var inv baselineInventory
	if err := json.Unmarshal(rawInv, &inv); err != nil {
		t.Fatalf("unmarshal baseline inventory: %v", err)
	}

	if inv.BaselineSHA != "95089eb4b74d5cf8d062f238a1121124ce0da878" {
		t.Fatalf("baseline SHA mismatch: want 95089eb4b74d5cf8d062f238a1121124ce0da878, got %q", inv.BaselineSHA)
	}
	if !slices.Equal(inv.BundledFrontendIDs, fe) {
		t.Fatalf("bundled frontends mismatch in inventory: want %v, got %v", fe, inv.BundledFrontendIDs)
	}
	if !slices.Equal(inv.BundledBackendIDs, be) {
		t.Fatalf("bundled backends mismatch in inventory: want %v, got %v", be, inv.BundledBackendIDs)
	}
	if inv.MatrixCellCount != len(cells) {
		t.Fatalf("matrix cell count mismatch: want %d, got %d", len(cells), inv.MatrixCellCount)
	}
	if inv.RequiredFeatureCount != len(reqFeatures) {
		t.Fatalf("required feature count mismatch: want %d, got %d", len(reqFeatures), inv.RequiredFeatureCount)
	}
	if !slices.Equal(inv.EssentialBackendKinds, essential) {
		t.Fatalf("essential backend kinds mismatch: want %v, got %v", essential, inv.EssentialBackendKinds)
	}
	if inv.TotalCartesianGoLines <= 0 || len(inv.CartesianFiles) == 0 {
		t.Fatalf("Cartesian inventory must contain positive LOC and files")
	}
	for _, required := range []string{"internal/testkit/conformance/backend_credentials_test.go", "internal/testkit/conformance/conformance_text_test.go", "internal/testkit/conformance/conformance_tools_test.go", "internal/testkit/conformance/conformance_multimodal_test.go", "internal/testkit/conformance/deployment.go", "internal/testkit/conformance/stage_checklist_14_evidence_test.go"} {
		found := false
		for _, file := range inv.CartesianFiles {
			if file.FilePath == required {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("inventory omitted explicitly required transitive surface %q", required)
		}
	}
	if !slices.Equal(inv.BundledFrontendIDs, BundledFrontendIDs()) || !slices.Equal(inv.BundledBackendIDs, BundledBackendIDs()) {
		t.Fatalf("inventory identity lists must match recomputed matrix ownership")
	}
	if !slices.Equal(inv.CompatibleBackendKinds, compatible) {
		t.Fatalf("compatible backend kinds mismatch: want %v, got %v", compatible, inv.CompatibleBackendKinds)
	}
	if !slices.Equal(inv.BackendPluginVersions, []string{"v1.0", "v1.1", "v1.2", "v1.3"}) {
		t.Fatalf("backend plugin versions mismatch: got %v", inv.BackendPluginVersions)
	}
	if inv.TotalCartesianGoLines <= 0 {
		t.Fatalf("total cartesian go lines must be positive, got %d", inv.TotalCartesianGoLines)
	}
	if len(inv.CartesianFiles) == 0 {
		t.Fatalf("expected non-empty Cartesian inventory")
	}

	// Verify inventory completeness: detect if any Cartesian file in internal/testkit/conformance is omitted
	if err := VerifyInventoryCompleteness(inv.CartesianFiles, filepath.Join("..", "..", "internal", "testkit", "conformance")); err != nil {
		t.Fatalf("inventory completeness check failed: %v", err)
	}
}

// BaselineCartesianCandidates derives Cartesian-owned files from the frozen git
// object. It uses parsed declarations/calls plus the owned row/column/matrix
// naming contract; it never scans the mutable worktree for marker strings.
func BaselineCartesianCandidates(repoRoot, sha string) ([]string, error) {
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", sha, "--", "internal/testkit/conformance")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list baseline conformance files: %w", err)
	}
	var candidates []string
	for _, path := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		path = strings.TrimSpace(path)
		if path == "" || !strings.HasSuffix(path, ".go") {
			continue
		}
		show := exec.Command("git", "show", sha+":"+path)
		show.Dir = repoRoot
		source, err := show.Output()
		if err != nil {
			return nil, fmt.Errorf("read baseline %s: %w", path, err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
		if err != nil {
			return nil, fmt.Errorf("parse baseline %s: %w", path, err)
		}
		owned := false
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if ident, ok := x.Fun.(*ast.Ident); ok && (ident.Name == "AllCells" || ident.Name == "BundledFrontendIDs" || ident.Name == "BundledBackendIDs") {
					owned = true
				}
			case *ast.Ident:
				if strings.Contains(x.Name, "MatrixCell") || strings.Contains(x.Name, "FrontendRow") || strings.Contains(x.Name, "BackendColumn") {
					owned = true
				}
			}
			return !owned
		})
		base := strings.ToLower(filepath.Base(filepath.FromSlash(path)))
		base = strings.ToLower(filepath.Base(strings.ReplaceAll(path, "\\", "/")))
		if strings.Contains(base, "matrix") || strings.Contains(base, "row_column") || strings.Contains(base, "frontend_row") || strings.Contains(base, "backend_column") || strings.Contains(base, "connector_columns") || strings.Contains(base, "dual_plane_economic") {
			owned = true
		}
		// Explicit release evidence and transitive consumers are frozen surfaces,
		// even when they do not mention the matrix authority directly.
		if base == "backend_credentials_test.go" || base == "conformance_text_test.go" || base == "conformance_tools_test.go" || base == "conformance_multimodal_test.go" || base == "deployment.go" || base == "deployment_test.go" || base == "stage_checklist_14_evidence_test.go" || strings.HasPrefix(base, "parity_") || base == "refparity.go" {
			owned = true
		}
		if strings.HasSuffix(base, "_test.go") && (strings.Contains(base, "row_") || strings.Contains(base, "column_") || strings.Contains(base, "matrix_") || strings.HasSuffix(base, "_parity_test.go") || base == "backend_credentials_test.go" || base == "conformance_text_test.go" || base == "conformance_tools_test.go" || base == "conformance_multimodal_test.go" || base == "deployment_test.go" || base == "stage_checklist_14_evidence_test.go") {
			owned = true
		}
		if owned {
			candidates = append(candidates, path)
		}
	}
	slices.Sort(candidates)
	return candidates, nil
}

// VerifyInventoryCompleteness compares the recorded inventory to deterministic
// baseline ownership and reports both omissions and unrecorded candidates.
func findRepoRoot(start string) (string, error) {
	path, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() {
		path = filepath.Dir(path)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(path, "go.mod")); statErr == nil {
			return path, nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", fmt.Errorf("repository root not found from %s", start)
		}
		path = parent
	}
}

func VerifyInventoryCompleteness(recordedFiles []cartesianFileDetail, conformanceDir string) error {
	recordedSet := make(map[string]bool, len(recordedFiles))
	for _, f := range recordedFiles {
		recordedSet[filepath.ToSlash(strings.TrimSpace(f.FilePath))] = true
	}
	repoRoot, err := findRepoRoot(conformanceDir)
	if err != nil {
		return err
	}
	candidates, err := BaselineCartesianCandidates(repoRoot, "95089eb4b74d5cf8d062f238a1121124ce0da878")
	if err != nil {
		return err
	}
	var missing []string
	for _, path := range candidates {
		path = strings.TrimSpace(path)
		if !recordedSet[path] {
			missing = append(missing, path)
		}
	}
	var extra []string
	for path := range recordedSet {
		found := slices.Contains(candidates, path)
		if !found {
			extra = append(extra, path)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf("baseline Cartesian inventory mismatch: missing=%v extra=%v", missing, extra)
	}
	return nil
}

// TestPhase1_Task11_DetectsOmittedCartesianFile verifies that VerifyInventoryCompleteness
// correctly detects and rejects inventory lists that omit actual Cartesian files in the codebase.
func TestPhase1_Task11_DetectsOmittedCartesianFile(t *testing.T) {
	t.Parallel()

	rawInv, err := os.ReadFile(filepath.Join("testdata", "baseline_cartesian_inventory.json"))
	if err != nil {
		t.Fatalf("read baseline inventory file: %v", err)
	}

	var inv baselineInventory
	if err := json.Unmarshal(rawInv, &inv); err != nil {
		t.Fatalf("unmarshal baseline inventory: %v", err)
	}

	if len(inv.CartesianFiles) == 0 {
		t.Fatalf("inventory CartesianFiles is empty")
	}

	// Mutate inventory by removing one Cartesian file
	incompleteFiles := append([]cartesianFileDetail(nil), inv.CartesianFiles[1:]...)

	conformanceDir := filepath.Join(".", ".")
	if err := VerifyInventoryCompleteness(incompleteFiles, conformanceDir); err == nil {
		t.Fatalf("expected VerifyInventoryCompleteness to detect omitted Cartesian file, but it passed")
	}
}

// TestPhase1_Task11_RecomputeGitBaselineInventory recomputes the baseline inventory
// deterministically from git object 95089eb4b74d5cf8d062f238a1121124ce0da878 using git show
// and AST analysis, verifying exact per-file non-generated Go LOC, function identities, and totals.
func TestPhase1_Task11_RecomputeGitBaselineInventory(t *testing.T) {
	t.Parallel()

	rawInv, err := os.ReadFile(filepath.Join("testdata", "baseline_cartesian_inventory.json"))
	if err != nil {
		t.Fatalf("read baseline inventory file: %v", err)
	}

	var expectedInv baselineInventory
	if err := json.Unmarshal(rawInv, &expectedInv); err != nil {
		t.Fatalf("unmarshal baseline inventory: %v", err)
	}

	gitSHA := expectedInv.BaselineSHA
	totalCalculatedLines := 0

	for _, expectedFile := range expectedInv.CartesianFiles {
		cmd := exec.Command("git", "show", gitSHA+":"+expectedFile.FilePath)
		content, err := cmd.Output()
		if err != nil {
			t.Fatalf("git show failed for %s at SHA %s: %v", expectedFile.FilePath, gitSHA, err)
		}

		lines, funcs, err := analyzeGoFileContent(expectedFile.FilePath, content)
		if err != nil {
			t.Fatalf("analyzeGoFileContent failed for %s: %v", expectedFile.FilePath, err)
		}

		totalCalculatedLines += lines

		if lines != expectedFile.NonGeneratedGoLines {
			t.Errorf("file %s non-generated line mismatch: inventory=%d, recomputed=%d",
				expectedFile.FilePath, expectedFile.NonGeneratedGoLines, lines)
		}

		if !slices.Equal(funcs, expectedFile.Functions) {
			t.Errorf("file %s function list mismatch:\n  inventory:  %v\n  recomputed: %v",
				expectedFile.FilePath, expectedFile.Functions, funcs)
		}
	}

	if totalCalculatedLines != expectedInv.TotalCartesianGoLines {
		t.Fatalf("total Cartesian Go lines mismatch: inventory=%d, recomputed=%d",
			expectedInv.TotalCartesianGoLines, totalCalculatedLines)
	}
}

func analyzeGoFileContent(filePath string, content []byte) (int, []string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filePath, content, parser.ParseComments)
	if err != nil {
		return 0, nil, err
	}

	var funcs []string
	for _, decl := range f.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		var funcName string
		if funcDecl.Recv == nil || len(funcDecl.Recv.List) == 0 {
			funcName = funcDecl.Name.Name
		} else {
			recvType := formatTypeExpr(funcDecl.Recv.List[0].Type)
			funcName = fmt.Sprintf("%s.%s", recvType, funcDecl.Name.Name)
		}
		funcs = append(funcs, funcName)
	}

	nonGenLines := 0
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		nonGenLines++
	}

	return nonGenLines, funcs, nil
}

func formatTypeExpr(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return "*" + formatTypeExpr(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return formatTypeExpr(t.X) + "." + t.Sel.Name
	default:
		return fmt.Sprintf("%T", expr)
	}
}

// TestPhase1_Task11_BackendPluginABI_IndependentVersionNegotiation tests backend-plugin
// protocol negotiation independently across v1.0, v1.1, v1.2, and v1.3 compatibility boundaries.
func TestPhase1_Task11_BackendPluginABI_IndependentVersionNegotiation(t *testing.T) {
	t.Parallel()

	// v1.0 Base Protocol (No Optional Features)
	t.Run("v1.0 Base Protocol", func(t *testing.T) {
		host := backendplugin.ProtocolOffer{Major: 1, Minor: 0, DisableTransportRetries: true}
		plugin := backendplugin.ProtocolOffer{Major: 1, Minor: 0, DisableTransportRetries: true}
		neg, err := backendplugin.Negotiate(host, plugin)
		if err != nil || !neg.Compatible || neg.NegotiatedMinor != 0 {
			t.Fatalf("v1.0 base negotiation failed: err=%v, neg=%+v", err, neg)
		}
	})

	// v1.1 Reasoning
	t.Run("v1.1 Reasoning Feature", func(t *testing.T) {
		host := backendplugin.ProtocolOffer{
			Major: 1, Minor: 1, DisableTransportRetries: true,
			Features: []backendplugin.Feature{{Name: backendplugin.FeatureExactReasoningParts, Required: false}},
		}
		plugin := backendplugin.ProtocolOffer{
			Major: 1, Minor: 1, DisableTransportRetries: true,
			Features: []backendplugin.Feature{{Name: backendplugin.FeatureExactReasoningParts, Required: false}},
		}
		neg, err := backendplugin.Negotiate(host, plugin)
		if err != nil || !neg.Compatible || neg.NegotiatedMinor != 1 || !slices.Contains(neg.EnabledFeatures, backendplugin.FeatureExactReasoningParts) {
			t.Fatalf("v1.1 reasoning negotiation failed: err=%v, neg=%+v", err, neg)
		}
	})

	// v1.2 Ordered Items
	t.Run("v1.2 Ordered Items Feature", func(t *testing.T) {
		host := backendplugin.ProtocolOffer{
			Major: 1, Minor: 2, DisableTransportRetries: true,
			Features: []backendplugin.Feature{{Name: backendplugin.FeatureOrderedItems, Required: false}},
		}
		plugin := backendplugin.ProtocolOffer{
			Major: 1, Minor: 2, DisableTransportRetries: true,
			Features: []backendplugin.Feature{{Name: backendplugin.FeatureOrderedItems, Required: false}},
		}
		neg, err := backendplugin.Negotiate(host, plugin)
		if err != nil || !neg.Compatible || neg.NegotiatedMinor != 2 || !slices.Contains(neg.EnabledFeatures, backendplugin.FeatureOrderedItems) {
			t.Fatalf("v1.2 ordered items negotiation failed: err=%v, neg=%+v", err, neg)
		}
	})

	// v1.3 Exact OpenResponses
	t.Run("v1.3 Exact OpenResponses Feature", func(t *testing.T) {
		host := backendplugin.ProtocolOffer{
			Major: 1, Minor: 3, DisableTransportRetries: true,
			Features: []backendplugin.Feature{{Name: backendplugin.FeatureExactOpenResponsesFields, Required: false}},
		}
		plugin := backendplugin.ProtocolOffer{
			Major: 1, Minor: 3, DisableTransportRetries: true,
			Features: []backendplugin.Feature{{Name: backendplugin.FeatureExactOpenResponsesFields, Required: false}},
		}
		neg, err := backendplugin.Negotiate(host, plugin)
		if err != nil || !neg.Compatible || neg.NegotiatedMinor != 3 || !slices.Contains(neg.EnabledFeatures, backendplugin.FeatureExactOpenResponsesFields) {
			t.Fatalf("v1.3 exact openresponses negotiation failed: err=%v, neg=%+v", err, neg)
		}
	})

	// Boundary: Major Version Mismatch
	t.Run("Major Mismatch Incompatible", func(t *testing.T) {
		host := backendplugin.ProtocolOffer{Major: 1, Minor: 3, DisableTransportRetries: true}
		plugin := backendplugin.ProtocolOffer{Major: 2, Minor: 0, DisableTransportRetries: true}
		neg, err := backendplugin.Negotiate(host, plugin)
		if err == nil && neg.Compatible {
			t.Fatalf("expected major version mismatch to be incompatible")
		}
	})

	// Boundary: Unknown Required Feature
	t.Run("Unknown Required Feature Incompatible", func(t *testing.T) {
		host := backendplugin.ProtocolOffer{Major: 1, Minor: 3, DisableTransportRetries: true}
		plugin := backendplugin.ProtocolOffer{
			Major: 1, Minor: 3, DisableTransportRetries: true,
			Features: []backendplugin.Feature{{Name: "unknown_mandatory_feature", Required: true}},
		}
		neg, err := backendplugin.Negotiate(host, plugin)
		if err == nil && neg.Compatible {
			t.Fatalf("expected unknown required feature to be incompatible")
		}
	})
}

// TestPhase1_Task11_ContinuationAuthorities_FullParityCharacterization fully characterizes
// both continuation authorities (pkg/lipsdk/continuation AND internal/core/continuation):
// MemoryStore and StreamRecorder happy path, lifecycle, scope isolation, expiry, and failure handling.
func TestPhase1_Task11_ContinuationAuthorities_FullParityCharacterization(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	scope1 := sdkcontinuation.Scope{TenantID: "tenant-A", PrincipalID: "p1"}
	scope2 := sdkcontinuation.Scope{TenantID: "tenant-B", PrincipalID: "p2"}
	// Keep happy-path retention independent from scheduler timing. Expiry is tested by
	// advancing each store's injected clock, not by waiting for wall time to pass.
	policy := sdkcontinuation.StoragePolicy{Mode: sdkcontinuation.PersistencePersistent, TTL: time.Hour}

	// 1. Characterize pkg/lipsdk/continuation.MemoryStore
	t.Run("SDK MemoryStore Characterization", func(t *testing.T) {
		store := sdkcontinuation.NewMemoryStore()
		sdkNow := time.Unix(1700000000, 0)
		store.SetClock(func() time.Time { return sdkNow })

		// Reserve and PutTerminal happy path
		id1, err := store.Reserve(ctx, scope1, policy)
		if err != nil {
			t.Fatalf("SDK Reserve failed: %v", err)
		}
		rec1 := sdkcontinuation.ContinuationRecord{
			ID:       id1,
			Scope:    scope1,
			Terminal: true,
			Status:   sdkcontinuation.RecordStatusCompleted,
		}
		if err := store.PutTerminal(ctx, rec1); err != nil {
			t.Fatalf("SDK PutTerminal failed: %v", err)
		}

		// Scope isolation: scope2 cannot get scope1's record
		if _, err := store.Get(ctx, scope2, id1); err == nil {
			t.Fatalf("expected Scope2 Get to fail for Scope1 record")
		}

		// Scope1 Get succeeds
		got, err := store.Get(ctx, scope1, id1)
		if err != nil || got.ID != id1 {
			t.Fatalf("SDK Get failed: err=%v, got=%+v", err, got)
		}

		// Deterministic expiry check: advance the injected clock beyond the happy-path TTL.
		sdkNow = sdkNow.Add(2 * time.Hour)
		if _, err := store.Get(ctx, scope1, id1); err == nil {
			t.Fatalf("expected Get to fail after TTL expiry")
		}

		// Close lifecycle
		if err := store.Close(); err != nil {
			t.Fatalf("SDK Close failed: %v", err)
		}
		if _, err := store.Reserve(ctx, scope1, policy); err != sdkcontinuation.ErrStoreClosed {
			t.Fatalf("expected ErrStoreClosed after Close, got %v", err)
		}
	})

	// 2. Characterize internal/core/continuation.MemoryStore
	t.Run("Core MemoryStore Characterization", func(t *testing.T) {
		store := continuation.NewMemoryStore()
		coreNow := time.Unix(1700000000, 0)
		store.SetClock(func() time.Time { return coreNow })

		id1, err := store.Reserve(ctx, scope1, policy)
		if err != nil {
			t.Fatalf("Core Reserve failed: %v", err)
		}
		rec1 := sdkcontinuation.ContinuationRecord{
			ID:       id1,
			Scope:    scope1,
			Terminal: true,
			Status:   sdkcontinuation.RecordStatusCompleted,
		}
		if err := store.PutTerminal(ctx, rec1); err != nil {
			t.Fatalf("Core PutTerminal failed: %v", err)
		}

		// Scope isolation
		if _, err := store.Get(ctx, scope2, id1); err == nil {
			t.Fatalf("expected Scope2 Get to fail for Scope1 record")
		}

		got, err := store.Get(ctx, scope1, id1)
		if err != nil || got.ID != id1 {
			t.Fatalf("Core Get failed: err=%v, got=%+v", err, got)
		}
		// Match SDK expiry characterization by advancing the injected clock deterministically.
		coreNow = coreNow.Add(2 * time.Hour)
		if _, err := store.Get(ctx, scope1, id1); err == nil {
			t.Fatalf("expected core Get to fail after TTL expiry")
		}

		// Close lifecycle
		if err := store.Close(); err != nil {
			t.Fatalf("Core Close failed: %v", err)
		}
		if _, err := store.Reserve(ctx, scope1, policy); err != sdkcontinuation.ErrStoreClosed {
			t.Fatalf("expected ErrStoreClosed after Close, got %v", err)
		}
	})

	// 3. Characterize StreamRecorder Observer Lifecycle (SDK & Core)
	t.Run("StreamRecorder Lifecycle Parity", func(t *testing.T) {
		store := sdkcontinuation.NewMemoryStore()
		id, err := store.Reserve(ctx, scope1, policy)
		if err != nil {
			t.Fatalf("Reserve failed: %v", err)
		}

		cleanedUp := false
		cleanup := func() { cleanedUp = true }
		rec := sdkcontinuation.ContinuationRecord{ID: id, Scope: scope1, Policy: policy}

		recorder := sdkcontinuation.NewStreamRecorder(sdkcontinuation.TerminalRecorder{Store: store}, rec, cleanup)
		recorder.Observe(ctx, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "Hello"})
		recorder.Observe(ctx, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"})

		if !recorder.ContinuationReservationCleanupConsumed() {
			t.Fatalf("expected StreamRecorder cleanup to be consumed after terminal observe")
		}

		got, err := store.Get(ctx, scope1, id)
		if err != nil || got.ID != id {
			t.Fatalf("expected recorded continuation in store, got err=%v", err)
		}

		// Incomplete stream close releases reservation
		id2, _ := store.Reserve(ctx, scope1, policy)
		rec2 := sdkcontinuation.ContinuationRecord{ID: id2, Scope: scope1, Policy: policy}
		recorder2 := sdkcontinuation.NewStreamRecorder(sdkcontinuation.TerminalRecorder{Store: store}, rec2, cleanup)
		_ = recorder2.Close()
		if !cleanedUp {
			t.Fatalf("expected Close on incomplete recorder to invoke cleanup")
		}

		// Exercise the core recorder against the same lifecycle and event sequence.
		coreStore := continuation.NewMemoryStore()
		coreID, err := coreStore.Reserve(ctx, scope1, policy)
		if err != nil {
			t.Fatalf("core recorder reserve failed: %v", err)
		}
		coreCleaned := false
		coreRec := sdkcontinuation.ContinuationRecord{ID: coreID, Scope: scope1, Policy: policy}
		coreRecorder := continuation.NewStreamRecorder(sdkcontinuation.TerminalRecorder{Store: coreStore}, coreRec, func() { coreCleaned = true })
		coreRecorder.Observe(ctx, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "Hello"})
		coreRecorder.Observe(ctx, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"})
		if !coreRecorder.ContinuationReservationCleanupConsumed() {
			t.Fatal("expected core recorder cleanup to be consumed")
		}
		if _, err := coreStore.Get(ctx, scope1, coreID); err != nil {
			t.Fatalf("expected core recorder terminal record: %v", err)
		}
		coreID2, _ := coreStore.Reserve(ctx, scope1, policy)
		coreIncomplete := continuation.NewStreamRecorder(sdkcontinuation.TerminalRecorder{Store: coreStore}, sdkcontinuation.ContinuationRecord{ID: coreID2, Scope: scope1, Policy: policy}, func() { coreCleaned = true })
		_ = coreIncomplete.Close()
		if !coreCleaned {
			t.Fatal("expected core incomplete close cleanup")
		}
	})
}
