package main_test

import (
	"context"
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
	cmd := exec.CommandContext(context.Background(), "go", "run", ".")
	cmd.Dir = dir
	cmd.Env = enterpriseTestEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "enterprise_module: ok") {
		t.Fatalf("output=%q", out)
	}
}

// enterpriseTestEnv forces GOWORK=off and clears LIP_ENTERPRISE_CONFIG so the
// fixture self-bootstraps an essential custom-openai-legacy-compatible upstream
// (no external connector discovery / dogfood staging).
func enterpriseTestEnv() []string {
	out := make([]string, 0, len(os.Environ())+2)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GOWORK=") || strings.HasPrefix(e, "LIP_ENTERPRISE_CONFIG=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, "GOWORK=off")
}
