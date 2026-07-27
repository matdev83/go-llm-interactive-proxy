package backendplugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// StageOpenRouter builds connectors/openrouter into a discovery root with a
// closed manifest suitable for BuildBootstrap discover→trust→install.
func StageOpenRouter(tb testing.TB) (pluginRoot string) {
	tb.Helper()
	repo := findRepoRoot(tb)
	root := tb.TempDir()
	binName := "lip-backend-openrouter"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	rel := filepath.ToSlash(filepath.Join("bin", binName))
	dst := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		tb.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", dst, "./cmd/lip-backend-openrouter")
	cmd.Dir = filepath.Join(repo, "connectors", "openrouter")
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("build openrouter: %v\n%s", err, out)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		tb.Fatal(err)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	body := fmt.Sprintf(`{
  "schema":"golip.backendplugin.manifest/v1",
  "plugin_id":"io.golip.backend.openrouter",
  "version":"0.1.0",
  "build_id":"test",
  "executable":%q,
  "sha256":%q,
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":%q,"arch":%q}],
  "exports":[{
    "kind":"openrouter",
    "credential_mode":"static",
    "access_scope":"any",
    "process_sharing":"per_instance"
  }]
}`, rel, digest, runtime.GOOS, runtime.GOARCH)
	if err := os.WriteFile(filepath.Join(root, "plugin.backendplugin.json"), []byte(body), 0o600); err != nil {
		tb.Fatal(err)
	}
	return root
}
