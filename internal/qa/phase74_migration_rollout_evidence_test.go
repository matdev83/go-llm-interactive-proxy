package qa

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	bpkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
)

func TestPhase74_MigrationRolloutDocPresent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc := filepath.Join(root, "docs", "dual-plane-migration-rollout.md")
	b, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("read migration doc: %v", err)
	}
	text := string(b)
	for _, needle := range []string{
		"EconomicControlReady",
		"distributed_strict",
		"advisory_single_process",
		"terminal-work",
		"CanRemoveProvider",
		"enterprise_module",
		"check-config",
		"Non-goals",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("dual-plane-migration-rollout.md missing %q", needle)
		}
	}
}

var (
	lipstdBinOnce sync.Once
	lipstdBinPath string
	lipstdBinErr  error
)

func getLipstdBinary(tb testing.TB, root string) string {
	tb.Helper()
	lipstdBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "lipstd-qa-bin-")
		if err != nil {
			lipstdBinErr = err
			return
		}
		name := "lipstd"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		dst := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-o", dst, "./cmd/lipstd")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			lipstdBinErr = fmt.Errorf("build lipstd: %w\n%s", err, out)
			return
		}
		lipstdBinPath = dst
	})
	if lipstdBinErr != nil {
		tb.Fatal(lipstdBinErr)
	}
	return lipstdBinPath
}

func TestPhase74_CheckConfigDogfoodStub(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	cfg := bpkit.WriteDogfoodLocalStubConfig(t)
	bin := getLipstdBinary(t, root)
	cmd := exec.Command(bin, "check-config", "--config", cfg)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("check-config dogfood-local-stub: %v\n%s", err, out)
	}
}

func TestPhase74_EnterpriseModuleUsesEconomicControlReady(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	src := filepath.Join(root, "testdata", "enterprise_module", "main.go")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "EconomicControlReady") {
		t.Fatal("enterprise_module must exercise public EconomicControlReady")
	}
	if strings.Contains(string(b), "internal/") {
		t.Fatal("enterprise_module must not import internal/")
	}
}
