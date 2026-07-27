package manifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	inframanifest "github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/manifest"
	sdkmanifest "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/manifest"
)

func validJSON() string {
	return `{
  "schema":"golip.backendplugin.manifest/v1",
  "plugin_id":"io.golip.example",
  "version":"1.0.0",
  "build_id":"b1",
  "executable":"bin/plugin",
  "sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":"linux","arch":"amd64"}],
  "exports":[{
    "kind":"example",
    "display_name":"Example",
    "description":"d",
    "credential_mode":"none",
    "access_scope":"local_only",
    "process_sharing":"per_instance"
  }]
}`
}

func TestParseStrict_ValidRoundTrip(t *testing.T) {
	t.Parallel()
	m, err := inframanifest.ParseStrictBytes([]byte(validJSON()))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if m.PluginID != "io.golip.example" || m.Exports[0].Kind != "example" {
		t.Fatalf("%+v", m)
	}
}

func TestParseStrict_UnknownField(t *testing.T) {
	t.Parallel()
	raw := strings.Replace(validJSON(), `"build_id":"b1"`, `"build_id":"b1","extra":1`, 1)
	_, err := inframanifest.ParseStrictBytes([]byte(raw))
	if err == nil || !strings.Contains(err.Error(), "unknown field") && err != sdkmanifest.ErrUnknownField {
		if err == nil {
			t.Fatal("expected error")
		}
	}
}

func TestParseStrict_ForbiddenFields(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"env", "args", "secrets", "url", "hooks", "shell", "models"} {
		raw := strings.Replace(validJSON(), `"build_id":"b1"`, `"build_id":"b1","`+field+`":{}`, 1)
		_, err := inframanifest.ParseStrictBytes([]byte(raw))
		if err == nil {
			t.Fatalf("%s: expected reject", field)
		}
	}
}

func TestParseStrict_DuplicateExport(t *testing.T) {
	t.Parallel()
	// duplicate export fixture:
	raw := `{
  "schema":"golip.backendplugin.manifest/v1",
  "plugin_id":"io.golip.example",
  "version":"1.0.0",
  "build_id":"b1",
  "executable":"bin/plugin",
  "sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "protocol_major":1,"protocol_min_minor":0,"protocol_max_minor":0,
  "platforms":[{"os":"linux","arch":"amd64"}],
  "exports":[
    {"kind":"example","credential_mode":"none","access_scope":"local_only","process_sharing":"per_instance"},
    {"kind":"example","credential_mode":"none","access_scope":"local_only","process_sharing":"per_instance"}
  ]
}`
	_, err := inframanifest.ParseStrictBytes([]byte(raw))
	if err == nil {
		t.Fatal("expected duplicate reject")
	}
}

func TestParseStrict_ScriptExecutableRejected(t *testing.T) {
	t.Parallel()
	raw := strings.Replace(validJSON(), `"bin/plugin"`, `"bin/run.sh"`, 1)
	_, err := inframanifest.ParseStrictBytes([]byte(raw))
	if err == nil {
		t.Fatal("expected script reject")
	}
}

func TestParseStrict_WindowsRequiresExe(t *testing.T) {
	t.Parallel()
	raw := strings.Replace(validJSON(), `"linux"`, `"windows"`, 1)
	_, err := inframanifest.ParseStrictBytes([]byte(raw))
	if err == nil {
		t.Fatal("expected .exe requirement")
	}
	raw = strings.Replace(raw, `"bin/plugin"`, `"bin/plugin.exe"`, 1)
	if _, err := inframanifest.ParseStrictBytes([]byte(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestParseStrict_FixtureValid(t *testing.T) {
	t.Parallel()
	p := filepath.Join("testdata", "valid_v1.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inframanifest.ParseStrictBytes(b); err != nil {
		t.Fatal(err)
	}
}

func TestParseStrict_Oversized(t *testing.T) {
	t.Parallel()
	big := []byte(`{"schema":"` + strings.Repeat("x", sdkmanifest.MaxManifestBytes) + `"}`)
	_, err := inframanifest.ParseStrictBytes(big)
	if err == nil {
		t.Fatal("expected bounds")
	}
}
