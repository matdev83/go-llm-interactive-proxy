package main_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnterpriseModule_BuildAndRun(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	repo := filepath.Clean(filepath.Join(dir, "..", ".."))
	cfg := os.Getenv("LIP_ENTERPRISE_CONFIG")
	if strings.TrimSpace(cfg) == "" {
		cfg = stageFromExample(t, repo)
	}
	cmd := exec.CommandContext(context.Background(), "go", "run", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "LIP_ENTERPRISE_CONFIG="+cfg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "enterprise_module: ok") {
		t.Fatalf("output=%q", out)
	}
}

func stageFromExample(t *testing.T, repo string) string {
	t.Helper()
	pluginRoot := stageLocalStub(t, repo)
	src := filepath.Join(repo, "config", "examples", "dogfood-local-stub.yaml")
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	const old = ".golip-plugins/full/localstub"
	if !strings.Contains(body, old) {
		t.Fatalf("example missing discovery path %q", old)
	}
	body = strings.ReplaceAll(body, old, filepath.ToSlash(pluginRoot))
	path := filepath.Join(t.TempDir(), "enterprise-dogfood.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func stageLocalStub(t *testing.T, repo string) string {
	t.Helper()
	root := t.TempDir()
	binName := "lip-backend-localstub"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	rel := filepath.ToSlash(filepath.Join("bin", binName))
	dst := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", dst, "./cmd/lip-backend-localstub")
	build.Dir = filepath.Join(repo, "connectors", "localstub")
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build localstub: %v\n%s", err, out)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	manifest := fmt.Sprintf(`{
  "schema":"golip.backendplugin.manifest/v1",
  "plugin_id":"io.golip.backend.localstub",
  "version":"0.1.0",
  "build_id":"enterprise-test",
  "executable":%q,
  "sha256":%q,
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":%q,"arch":%q}],
  "exports":[{
    "kind":"local-stub",
    "credential_mode":"none",
    "access_scope":"any",
    "process_sharing":"per_instance"
  }]
}`, rel, hex.EncodeToString(sum[:]), runtime.GOOS, runtime.GOARCH)
	if err := os.WriteFile(filepath.Join(root, "plugin.backendplugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}
