package qa

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCrossPlatformSelection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("selector uses the repository's POSIX bash runtime")
	}

	root := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	runGit("init", "-q")
	runGit("config", "user.email", "qa@example.com")
	runGit("config", "user.name", "QA")
	for _, path := range []string{"connectors/foo/release.yaml", "connectors/bar/release.yaml", "base.txt"} {
		writeSelectionFixture(t, root, path)
	}
	runGit("add", ".")
	runGit("commit", "-qm", "base")
	base := gitRevision(t, root)

	script := filepath.Join(root, "cross-platform-selection.sh")
	contents, err := os.ReadFile(repositoryFile(t, "scripts", "cross-platform-selection.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, contents, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		want string
	}{
		{name: "connector", path: "connectors/foo/source.go", want: "foo"},
		{name: "support", path: "connector-support/acp/client.go", want: "acp,agycliacp,codex,cursorcliacp,geminicliacp"},
		{name: "module metadata", path: "connectors/foo/go.mod", want: ""},
		{name: "docs only", path: "docs.md", want: ""},
		{name: "unknown connector", path: "connectors/new/source.go", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runGit("checkout", "-q", base)
			writeSelectionFixture(t, root, tc.path)
			runGit("add", ".")
			runGit("commit", "-qm", tc.name)
			head := gitRevision(t, root)

			cmd := exec.Command("bash", script, base, head)
			cmd.Dir = root
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("selector: %v\n%s", err, output)
			}
			if got := strings.TrimSpace(string(output)); got != tc.want {
				t.Fatalf("selection = %q, want %q", got, tc.want)
			}
		})
	}
}

func writeSelectionFixture(t *testing.T, root, path string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitRevision(t *testing.T, root string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output))
}
