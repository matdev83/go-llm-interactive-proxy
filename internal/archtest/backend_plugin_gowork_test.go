package archtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

type goworkOffCmd struct {
	Name string
	Args []string
	Dir  string
	Env  []string
}

func buildGOWORKOffCommandPlan(root, tempDir string) []goworkOffCmd {
	env := append(os.Environ(), "GOWORK=off")
	binaryName := "lipstd"
	if runtime.GOOS == "windows" {
		binaryName = "lipstd.exe"
	}
	return []goworkOffCmd{
		{Name: "go", Args: []string{"list", "./..."}, Dir: root, Env: env},
		{Name: "go", Args: []string{"list", "-m", "all"}, Dir: root, Env: env},
		{Name: "go", Args: []string{"build", "-o", filepath.Join(tempDir, binaryName), "./cmd/lipstd"}, Dir: root, Env: env},
		{
			Name: "go",
			Args: []string{
				"test", "-run=^$", "-count=1",
				"./pkg/lipapi/...",
				"./pkg/lipsdk/...",
				"./api/backendplugin/...",
				"./cmd/lipstd",
			},
			Dir: root,
			Env: env,
		},
	}
}

func runGOWORKOffCommandPlan(t *testing.T, plan []goworkOffCmd) {
	t.Helper()
	for _, step := range plan {
		cmd := exec.Command(step.Name, step.Args...)
		cmd.Dir = step.Dir
		cmd.Env = step.Env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", step.Name, step.Args, err, out)
		}
	}
}

func TestGOWORKOff_CommandPlanSafety(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir1, dir2 := filepath.Join(t.TempDir(), "1"), filepath.Join(t.TempDir(), "2")
	p1, p2 := buildGOWORKOffCommandPlan(root, dir1), buildGOWORKOffCommandPlan(root, dir2)

	if len(p1) != 4 {
		t.Fatalf("want 4 commands, got %d", len(p1))
	}
	binaryName := "lipstd"
	if runtime.GOOS == "windows" {
		binaryName = "lipstd.exe"
	}
	wantCommands := [][]string{
		{"list", "./..."},
		{"list", "-m", "all"},
		{"build", "-o", filepath.Join(dir1, binaryName), "./cmd/lipstd"},
		{"test", "-run=^$", "-count=1", "./pkg/lipapi/...", "./pkg/lipsdk/...", "./api/backendplugin/...", "./cmd/lipstd"},
	}
	for i, step := range p1 {
		if step.Name != "go" || step.Dir != root || !slices.Contains(step.Env, "GOWORK=off") {
			t.Errorf("step %d invalid environment/dir: name=%s dir=%s", i, step.Name, step.Dir)
		}
		if !slices.Equal(step.Args, wantCommands[i]) {
			t.Errorf("step %d args mismatch: got %v, want %v", i, step.Args, wantCommands[i])
		}
	}
	if p1[2].Args[2] == p2[2].Args[2] || !strings.HasPrefix(p1[2].Args[2], dir1) {
		t.Errorf("build outputs not distinct per invocation: p1=%s, p2=%s", p1[2].Args[2], p2[2].Args[2])
	}
}
