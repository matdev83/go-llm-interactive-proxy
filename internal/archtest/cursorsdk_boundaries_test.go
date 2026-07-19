package archtest

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCursorSDKNpmImportsStayInsideBridgeBoundary proves requirement 12.6:
// `@cursor/sdk` module imports/requires remain inside the Node bridge package only.
// Go pin strings in error/docs are allowed; Node import sites outside bridge/ are not.
func TestCursorSDKNpmImportsStayInsideBridgeBoundary(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	sdkRoot := filepath.Join(root, "internal", "plugins", "backends", "cursorsdk")
	bridgeRoot := filepath.Join(sdkRoot, "bridge")
	var offenders []string
	err := filepath.WalkDir(sdkRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".ts", ".tsx", ".js", ".mjs", ".cjs", ".json":
		default:
			return nil
		}
		// package-lock under bridge is expected to resolve @cursor/sdk.
		rel, err := filepath.Rel(bridgeRoot, path)
		underBridge := err == nil && !strings.HasPrefix(rel, "..")
		if underBridge {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(raw)
		if !strings.Contains(text, "@cursor/sdk") {
			return nil
		}
		// Only flag actual module import/require/dependency sites outside bridge/.
		if strings.Contains(text, `from "@cursor/sdk"`) ||
			strings.Contains(text, `from '@cursor/sdk'`) ||
			strings.Contains(text, `import("@cursor/sdk")`) ||
			strings.Contains(text, `import('@cursor/sdk')`) ||
			strings.Contains(text, `require("@cursor/sdk")`) ||
			strings.Contains(text, `require('@cursor/sdk')`) ||
			strings.Contains(text, `"@cursor/sdk":`) ||
			strings.Contains(text, `'@cursor/sdk':`) {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk cursorsdk: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("@cursor/sdk import/require sites must stay under bridge/; offenders=%v", offenders)
	}
}

// TestCoreAndProviderBoundaryDoNotImportCursorSDKBackend keeps core free of the
// concrete cursorsdk plugin (provider semantics stay in adapters).
func TestCoreAndProviderBoundaryDoNotImportCursorSDKBackend(t *testing.T) {
	t.Parallel()
	patterns := []string{
		"./internal/core/...",
		"./pkg/lipapi/...",
		"./pkg/lipsdk/...",
	}
	cmd := exec.Command("go", append([]string{"list", "-json", "-test=false"}, patterns...)...)
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, imp := range pkg.Imports {
			if strings.Contains(imp, "/internal/plugins/backends/cursorsdk") {
				t.Fatalf("package %q must not import cursorsdk backend %q", pkg.ImportPath, imp)
			}
		}
	}
}
