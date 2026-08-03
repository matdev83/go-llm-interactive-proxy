package archtest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// complianceToolRel is the isolated test-only JS tool directory.
const complianceToolRel = "tools/openresponses-compliance"

// complianceSuiteDigest is the pinned digest of the official compliance suite
// recorded in the protocol testdata manifest (official_2026-04-24_manifest.json).
const complianceSuiteDigest = "63b5e6595ac831ee74b8e887af76c28d69aee8e2ec7d9e99dc688eec4bccb7fb"

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestOpenResponsesJSComplianceToolIsolated keeps the official JS compliance
// runner out of the production/root Go module and pins its sources and
// dependencies:
//
//  1. the tool directory and its required pinned files exist,
//  2. the vendored official compliance-tests.ts digest matches the pinned
//     protocol manifest digest (immutable upstream source),
//  3. no Go package under cmd/internal/pkg imports the tool,
//  4. package.json pins exact dependency versions and package-lock.json records
//     integrity hashes (no mutable/unpinned dependencies),
//  5. the `-static` compliance gate (wired into `make qa`) never invokes a
//     JavaScript runtime,
//  6. production (non-test) Go code performs no node/bun/npm execution.
func TestOpenResponsesJSComplianceToolIsolated(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	tool := filepath.Join(root, filepath.FromSlash(complianceToolRel))

	required := []string{
		"package.json",
		"package-lock.json",
		"MANIFEST.json",
		"README.md",
		"LICENSE",
		"bin/compliance-test.ts",
		"scripts/run.mjs",
		"src/lib/compliance-tests.ts",
		"src/lib/sse-parser.ts",
		"src/generated/kubb/zod/index.ts",
	}
	for _, rel := range required {
		if _, err := os.Stat(filepath.Join(tool, filepath.FromSlash(rel))); err != nil {
			t.Errorf("compliance tool missing required file %s: %v", rel, err)
		}
	}
	// node_modules is a setup artifact (npm ci); it must never be committed.
	if gitIgnore := readFileString(t, filepath.Join(tool, ".gitignore")); !strings.Contains(gitIgnore, "node_modules/") {
		t.Error("compliance tool .gitignore must exclude node_modules/")
	}

	// 2. Vendored suite digest == pinned protocol manifest digest.
	vendored, err := os.ReadFile(filepath.Join(tool, "src", "lib", "compliance-tests.ts"))
	if err != nil {
		t.Fatalf("read vendored compliance-tests.ts: %v", err)
	}
	if got := sha256Hex(vendored); got != complianceSuiteDigest {
		t.Errorf("vendored compliance-tests.ts digest = %s, want pinned %s (source must stay immutable)", got, complianceSuiteDigest)
	}
	// The tool copy must be byte-identical to the canonical testdata copy.
	canonical, err := os.ReadFile(filepath.Join(root, "internal/plugins/protocols/openresponses/testdata/compliance/compliance-tests.ts"))
	if err != nil {
		t.Fatalf("read canonical testdata compliance-tests.ts: %v", err)
	}
	if string(vendored) != string(canonical) {
		t.Error("compliance tool copy of compliance-tests.ts diverges from canonical testdata copy")
	}

	// 3. No production Go package imports the tool.
	importErr := WalkProductionGoFiles(root, func(rel, _ string, src []byte) error {
		_, f, perr := ParseGoSource(rel, src)
		if perr != nil {
			return perr
		}
		for _, imp := range FileImportPaths(f) {
			if strings.Contains(imp, "openresponses-compliance") {
				t.Errorf("%s imports JS compliance tool path %q", rel, imp)
			}
		}
		return nil
	})
	if importErr != nil {
		t.Fatalf("scan production imports: %v", importErr)
	}

	// 4. Exact dependency pins with integrity.
	pkgJSON := readFileString(t, filepath.Join(tool, "package.json"))
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal([]byte(pkgJSON), &pkg); err != nil {
		t.Fatalf("parse package.json: %v", err)
	}
	pinned := map[string]string{"zod": "3.25.76", "esbuild": "0.28.1", "ws": "8.21.1"}
	for name, wantVersion := range pinned {
		got, ok := pkg.Dependencies[name]
		if !ok {
			got, ok = pkg.DevDependencies[name]
		}
		if !ok {
			t.Errorf("compliance tool missing pinned dependency %q", name)
			continue
		}
		if got != wantVersion {
			t.Errorf("compliance tool dependency %q version = %q, want exact %q", name, got, wantVersion)
		}
		if strings.ContainsAny(got, "^~") {
			t.Errorf("compliance tool dependency %q is not exactly pinned: %q", name, got)
		}
	}
	lock := readFileString(t, filepath.Join(tool, "package-lock.json"))
	if !strings.Contains(lock, `"integrity":`) {
		t.Error("package-lock.json must record dependency integrity hashes")
	}
	for _, name := range []string{"zod", "esbuild", "ws"} {
		if !strings.Contains(lock, `"node_modules/`+name+`"`) {
			t.Errorf("package-lock.json missing pinned entry for %q", name)
		}
	}

	// 5. The static compliance gate (wired into make qa) must not require a JS
	//    runtime; only the full scripts may invoke Node. The full scripts must
	//    run the ACTUAL suite separately, gated by LIP_RUN_OFFICIAL_COMPLIANCE.
	ps1 := readFileString(t, filepath.Join(root, "scripts/test-openresponses-compliance.ps1"))
	sh := readFileString(t, filepath.Join(root, "scripts/test-openresponses-compliance.sh"))
	for _, src := range []string{ps1, sh} {
		if !strings.Contains(src, "LIP_RUN_OFFICIAL_COMPLIANCE") {
			t.Error("compliance script must gate the actual official suite behind LIP_RUN_OFFICIAL_COMPLIANCE")
		}
	}
	if !strings.Contains(ps1, "Invoke-OfficialComplianceSuite") || !strings.Contains(sh, "invoke_official_compliance_suite") {
		t.Error("compliance scripts must invoke the ACTUAL official suite separately from the Go-native mirrors")
	}
	ps1Static := staticBlock(ps1, "if ($static) {")
	shStatic := staticBlock(sh, `if [[ "$STATIC" == "1" ]]; then`)
	for _, src := range []string{ps1Static, shStatic} {
		if strings.Contains(src, "Invoke-OfficialComplianceSuite") ||
			strings.Contains(src, "invoke_official_compliance_suite") ||
			strings.Contains(src, "Prepare-OfficialComplianceTooling") ||
			strings.Contains(src, "prepare_official_compliance_tooling") {
			t.Error("static compliance gate must not invoke the actual official suite (JS runtime)")
		}
	}

	// 6. Production (non-test) Go code performs no JS runtime execution.
	execErr := WalkProductionGoFiles(root, func(rel, _ string, src []byte) error {
		text := string(src)
		for _, needle := range []string{`exec.Command("node"`, `exec.Command("bun"`, `exec.Command("npm"`} {
			if strings.Contains(text, needle) {
				t.Errorf("%s invokes a JS runtime in production code: %s", rel, needle)
			}
		}
		return nil
	})
	if execErr != nil {
		t.Fatalf("scan production exec: %v", execErr)
	}
}

// staticBlock returns the script slice between startMarker and the first
// following "exit 0", i.e. the fast static gate body.
func staticBlock(src, startMarker string) string {
	i := strings.Index(src, startMarker)
	if i < 0 {
		return ""
	}
	body := src[i:]
	if j := strings.Index(body, "exit 0"); j >= 0 {
		return body[:j+len("exit 0")]
	}
	return body
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
