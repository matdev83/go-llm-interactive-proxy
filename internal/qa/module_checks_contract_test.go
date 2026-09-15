package qa

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The module check helper runs each module through an exported bash function in
// a freshly spawned `bash -c` process. A fresh bash process does not inherit
// errexit from the parent script, so per-module failures only propagate when the
// helper re-enables it. Without that, the helper's trailing `if [[ -d cmd ]]`
// absorbs the exit status and the script reports success for broken modules.
func TestCIModuleChecks_PerModuleFailuresPropagate(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		// Bash is not guaranteed on Windows; the remote Ubuntu workflow executes
		// the authoritative shell check.
		return
	}

	root := t.TempDir()
	writeModuleCheckFixture(t,
		filepath.Join(root, "scripts", "check-all-modules.sh"),
		readRepositoryFile(t, "scripts", "check-all-modules.sh"),
		0o755,
	)

	// A stub discovery binary keeps the check limited to the fixture module and
	// avoids building the real discovery tool inside the fixture root.
	discovery := filepath.Join(root, "discover-modules-stub.sh")
	writeModuleCheckFixture(t, discovery, "#!/usr/bin/env bash\nexit 0\n", 0o755)

	moduleDir := filepath.Join(root, "testdata", "external_feature_sdk")
	writeModuleCheckFixture(t,
		filepath.Join(moduleDir, "go.mod"),
		"module example.com/external_feature_sdk\n\ngo 1.22\n",
		0o644,
	)
	fixture := filepath.Join(moduleDir, "fixture_test.go")

	run := func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", filepath.Join(root, "scripts", "check-all-modules.sh"))
		cmd.Env = append(os.Environ(), "LIP_DISCOVER_MODULES_BIN="+discovery)
		return cmd.CombinedOutput()
	}

	// The healthy module must pass first: this proves the fixture itself is tidy
	// and that any later failure is the failing test and not module metadata.
	writeModuleCheckFixture(t, fixture,
		"package fixture\n\nimport \"testing\"\n\nfunc TestFixture(t *testing.T) {}\n",
		0o644,
	)
	output, err := run()
	if err != nil {
		t.Fatalf("check-all-modules.sh failed for a healthy module: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "OK: all discovered Go modules passed tidy, tests, and command builds") {
		t.Fatalf("healthy module run missing success marker: %q", output)
	}

	writeModuleCheckFixture(t, fixture,
		"package fixture\n\nimport \"testing\"\n\nfunc TestFixture(t *testing.T) {\n\tt.Fatal(\"module failure must not be masked\")\n}\n",
		0o644,
	)
	output, err = run()
	if err == nil {
		t.Fatalf("check-all-modules.sh reported success for a module whose tests fail:\n%s", output)
	}
	if !strings.Contains(string(output), "module failure must not be masked") {
		t.Fatalf("module failure output was not surfaced: %q", output)
	}
}

func writeModuleCheckFixture(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}
