package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalModuleFile_NormalizesCRLF(t *testing.T) {
	t.Parallel()
	lf := []byte("module example.com/m\n\ngo 1.22\n")
	crlf := []byte("module example.com/m\r\n\r\ngo 1.22\r\n")
	crOnly := []byte("module example.com/m\r\rgo 1.22\r")

	if !bytes.Equal(canonicalModuleFile(lf), canonicalModuleFile(crlf)) {
		t.Fatal("CRLF and LF forms must compare equal after canonicalization")
	}
	if !bytes.Equal(canonicalModuleFile(lf), canonicalModuleFile(crOnly)) {
		t.Fatal("CR-only and LF forms must compare equal after canonicalization")
	}
	if bytes.Contains(canonicalModuleFile(crlf), []byte{'\r'}) {
		t.Fatal("canonical form must not retain CR")
	}
}

func TestModuleFilesEqual_CRLFOnlyPasses(t *testing.T) {
	t.Parallel()
	modLF := []byte("module example.com/m\n\ngo 1.22\n")
	modCRLF := []byte("module example.com/m\r\n\r\ngo 1.22\r\n")
	sumLF := []byte("example.com/dep v1.0.0 h1:abc=\nexample.com/dep v1.0.0/go.mod h1:def=\n")
	sumCRLF := []byte("example.com/dep v1.0.0 h1:abc=\r\nexample.com/dep v1.0.0/go.mod h1:def=\r\n")

	if err := moduleFilesDrift("go.mod", modCRLF, modLF); err != nil {
		t.Fatalf("CRLF-only go.mod must not count as drift: %v", err)
	}
	if err := moduleFilesDrift("go.sum", sumCRLF, sumLF); err != nil {
		t.Fatalf("CRLF-only go.sum must not count as drift: %v", err)
	}
}

func TestModuleFilesEqual_OneTokenFails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		current []byte
		tidied  []byte
	}{
		{
			name:    "go.mod require version drift",
			current: []byte("module example.com/m\n\ngo 1.22\n\nrequire example.com/dep v1.0.0\n"),
			tidied:  []byte("module example.com/m\n\ngo 1.22\n\nrequire example.com/dep v1.0.1\n"),
		},
		{
			name:    "go.sum checksum token drift",
			current: []byte("example.com/dep v1.0.0 h1:AAAA\n"),
			tidied:  []byte("example.com/dep v1.0.0 h1:BBBB\n"),
		},
		{
			name:    "go.sum line order drift",
			current: []byte("a v1 h1:x\nb v1 h1:y\n"),
			tidied:  []byte("b v1 h1:y\na v1 h1:x\n"),
		},
		{
			name:    "go.mod content with CRLF still detects version drift",
			current: []byte("module example.com/m\r\n\r\ngo 1.22\r\n\r\nrequire example.com/dep v1.0.0\r\n"),
			tidied:  []byte("module example.com/m\n\ngo 1.22\n\nrequire example.com/dep v1.0.1\n"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := moduleFilesDrift("module", tt.current, tt.tidied); err == nil {
				t.Fatal("expected semantic drift to fail")
			}
		})
	}
}

func TestTidyDiffOnlyLineEndings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		diff string
		want bool
	}{
		{
			name: "crlf rewrite of go.sum lines",
			diff: "" +
				"diff current/go.sum tidy/go.sum\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -1,2 +1,2 @@\n" +
				"-example.com/dep v1.0.0 h1:abc=\r\n" +
				"-example.com/dep v1.0.0/go.mod h1:def=\r\n" +
				"+example.com/dep v1.0.0 h1:abc=\n" +
				"+example.com/dep v1.0.0/go.mod h1:def=\n",
			want: true,
		},
		{
			name: "checksum token change",
			diff: "" +
				"diff current/go.sum tidy/go.sum\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -1 +1 @@\n" +
				"-example.com/dep v1.0.0 h1:AAAA\n" +
				"+example.com/dep v1.0.0 h1:BBBB\n",
			want: false,
		},
		{
			name: "security error is not eol-only",
			diff: "verifying example.com/dep@v1.0.0: checksum mismatch\nSECURITY ERROR\n",
			want: false,
		},
		{
			name: "order drift",
			diff: "" +
				"diff current/go.sum tidy/go.sum\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -1,2 +1,2 @@\n" +
				"-a v1 h1:x\n" +
				"-b v1 h1:y\n" +
				"+b v1 h1:y\n" +
				"+a v1 h1:x\n",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tidyDiffOnlyLineEndings([]byte(tt.diff))
			if got != tt.want {
				t.Fatalf("tidyDiffOnlyLineEndings()=%v want %v", got, tt.want)
			}
		})
	}
}

func TestCheckModuleTidy_CRLFCheckoutPasses(t *testing.T) {
	modRoot := writeTinyModule(t, withCRLFModuleFiles(true))
	if _, err := checkModuleTidy(modRoot); err != nil {
		t.Fatalf("tidy check must pass for CRLF-only module files: %v", err)
	}
	// Source worktree must remain untouched (still CRLF).
	modBytes, err := os.ReadFile(filepath.Join(modRoot, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(modBytes, []byte("\r\n")) {
		t.Fatal("checkModuleTidy must not rewrite source go.mod line endings")
	}
}

func TestCheckModuleTidy_ChecksumDriftFails(t *testing.T) {
	modRoot := writeTinyModule(t, withCRLFModuleFiles(false))
	sumPath := filepath.Join(modRoot, "go.sum")
	sum, err := os.ReadFile(sumPath)
	if err != nil {
		t.Fatal(err)
	}
	// Inject an unused sum line tidy will drop — semantic drift, no network.
	corrupted := append(append([]byte{}, sum...), []byte("example.com/nonexistent v0.0.0 h1:DEADBEEFdeadbeefdeadbeefdeadbeefdeadbeef=\n")...)
	if err := os.WriteFile(sumPath, corrupted, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := checkModuleTidy(modRoot); err == nil {
		t.Fatal("expected checksum/module drift to fail tidy check")
	}
}

func TestCheckModuleTidy_GoModRequireDriftFails(t *testing.T) {
	modRoot := writeTinyModule(t, withCRLFModuleFiles(false))
	modPath := filepath.Join(modRoot, "go.mod")
	// Empty require block is dropped by tidy — semantic go.mod drift, no network.
	drifted := []byte("module example.com/releasegates/tidycase\n\ngo 1.22\n\nrequire (\n)\n")
	if err := os.WriteFile(modPath, drifted, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := checkModuleTidy(modRoot); err == nil {
		t.Fatal("expected go.mod require drift to fail tidy check")
	}
}

type tinyModuleOpts struct {
	crlf bool
}

func withCRLFModuleFiles(crlf bool) tinyModuleOpts {
	return tinyModuleOpts{crlf: crlf}
}

func writeTinyModule(t *testing.T, opts tinyModuleOpts) string {
	t.Helper()
	dir := t.TempDir()
	mod := "module example.com/releasegates/tidycase\n\ngo 1.22\n"
	sum := ""
	src := "package tidycase\n\nfunc Hello() string { return \"hi\" }\n"
	if opts.crlf {
		mod = strings.ReplaceAll(mod, "\n", "\r\n")
		sum = strings.ReplaceAll(sum, "\n", "\r\n")
		src = strings.ReplaceAll(src, "\n", "\r\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), []byte(sum), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "doc.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
