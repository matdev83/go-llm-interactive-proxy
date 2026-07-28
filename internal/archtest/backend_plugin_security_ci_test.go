package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackendPluginSecurity_makefileAndCIWired(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	mk := string(makefile)
	if !strings.Contains(mk, "backend-plugin-security-checks:") {
		t.Fatal("Makefile missing backend-plugin-security-checks target")
	}
	if !strings.Contains(mk, "FuzzManifest") || !strings.Contains(mk, "FuzzServerFrame") {
		t.Fatal("Makefile security/fuzz targets must include FuzzManifest and FuzzServerFrame")
	}
	if !strings.Contains(mk, "TestBuild_localOnly") || !strings.Contains(mk, "./internal/infra/runtimebundle/") {
		t.Fatal("backend-plugin-security-checks must run runtimebundle LocalOnly/credential security tests")
	}
	if !strings.Contains(mk, "./internal/infra/diagredact/") {
		t.Fatal("backend-plugin-security-checks must run diagredact package")
	}

	qa, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(".github/workflows/qa.yml")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(qa), "make backend-plugin-security-checks") {
		t.Fatal("qa.yml must wire make backend-plugin-security-checks")
	}

	nightly, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(".github/workflows/race-fuzz-nightly.yml")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(nightly), "make backend-plugin-security-checks") {
		t.Fatal("race-fuzz-nightly.yml must wire make backend-plugin-security-checks")
	}
	if !strings.Contains(string(nightly), "ubuntu-latest") {
		t.Fatal("race-fuzz-nightly must remain Linux for race evidence")
	}

	threat := filepath.Join(root, filepath.FromSlash("docs/backend-plugins/threat-model.md"))
	if _, err := os.Stat(threat); err != nil {
		t.Fatalf("threat-model.md required: %v", err)
	}
	evidence := filepath.Join(root, filepath.FromSlash(".kiro/specs/archive/backend-connector-plugin-architecture/phase9-task93-external-security-blocker.md"))
	if _, err := os.Stat(evidence); err != nil {
		t.Fatalf("task 9.3 archived security evidence required: %v", err)
	}
}
