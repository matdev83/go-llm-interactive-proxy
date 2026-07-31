package archtest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCursorSDKNpmImportsStayInsideBridgeBoundary proves requirement 12.6:
// `@cursor/sdk` module imports/requires remain inside the Node bridge companion only.
// Go pin strings in error/docs are allowed; Node import sites outside bridge-node/ are not.
func TestCursorSDKNpmImportsStayInsideBridgeBoundary(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	sdkRoot := filepath.Join(root, "connectors", "cursorsdk")
	bridgeRoot := filepath.Join(sdkRoot, "bridge-node")
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
		t.Fatalf("@cursor/sdk import/require sites must stay under bridge-node/; offenders=%v", offenders)
	}
}

// TestCoreAndProviderBoundaryDoNotImportCursorSDKBackend keeps core free of the
// concrete cursorsdk connector (provider semantics stay in adapters/connectors).
func TestCoreAndProviderBoundaryDoNotImportCursorSDKBackend(t *testing.T) {
	t.Parallel()
	patterns := []string{
		"./internal/core/...",
		"./pkg/lipapi/...",
		"./pkg/lipsdk/...",
	}
	out, err := cachedGoList(t, append([]string{"-json", "-test=false"}, patterns...)...)
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
			if strings.Contains(imp, "/internal/plugins/backends/cursorsdk") ||
				strings.Contains(imp, "/connectors/cursorsdk") {
				t.Fatalf("package %q must not import cursorsdk %q", pkg.ImportPath, imp)
			}
		}
	}
}
