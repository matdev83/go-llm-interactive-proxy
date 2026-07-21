package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
)

func TestStrictReloadDecode_TableFixtures(t *testing.T) {
	t.Parallel()

	pluginYAML := []byte(`
server:
  address: "127.0.0.1:0"
plugins:
  backends:
    - id: stub
      enabled: true
      config:
        foo: bar
        nested:
          x: 1
          list: [a, b]
`)

	cases := []struct {
		name    string
		raw     []byte
		want    configsource.Category
		checkOK func(t *testing.T, cfg *config.Config)
	}{
		{
			name: "malformed_yaml",
			raw:  []byte("server: [\n"),
			want: configsource.CategoryMalformedYAML,
		},
		{
			name: "multiple_documents",
			raw: []byte(`server:
  address: "127.0.0.1:0"
---
server:
  address: "127.0.0.1:1"
`),
			want: configsource.CategoryMultipleDocuments,
		},
		{
			name: "trailing_content",
			raw: []byte(`server:
  address: "127.0.0.1:0"
...
this is not yaml document content that decoder may still see
`),
			want: configsource.CategoryTrailingContent,
		},
		{
			name: "unknown_core_field",
			raw: []byte(`server:
  address: "127.0.0.1:0"
not_a_core_field: true
`),
			want: configsource.CategoryUnknownCoreField,
		},
		{
			name: "valid_opaque_plugin_yaml_node",
			raw:  pluginYAML,
			want: configsource.CategoryOK,
			checkOK: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if len(cfg.Plugins.Backends) != 1 {
					t.Fatalf("backends=%d", len(cfg.Plugins.Backends))
				}
				n := cfg.Plugins.Backends[0].Config
				if n.Kind == 0 {
					t.Fatal("expected preserved opaque plugin yaml.Node")
				}
				var into map[string]any
				if err := n.Decode(&into); err != nil {
					t.Fatal(err)
				}
				if into["foo"] != "bar" {
					t.Fatalf("plugin config: %#v", into)
				}
			},
		},
		{
			name: "empty_rejected_before_decode",
			raw:  []byte{},
			want: configsource.CategoryEmpty,
		},
		{
			name: "whitespace_rejected_before_decode",
			raw:  []byte("  \n\t\n"),
			want: configsource.CategoryWhitespace,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, cat, err := strictDecodeContract(tc.raw)
			if cat != tc.want {
				t.Fatalf("category: got %q want %q (err=%v)", cat, tc.want, err)
			}
			if tc.want == configsource.CategoryOK {
				if err != nil || cfg == nil {
					t.Fatalf("want OK, err=%v cfg=%v", err, cfg)
				}
				if tc.checkOK != nil {
					tc.checkOK(t, cfg)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			msg := err.Error()
			if strings.Contains(msg, "not_a_core_field: true") || strings.Contains(msg, "foo: bar") {
				t.Fatalf("error leaked raw config content: %q", msg)
			}
		})
	}
}

func TestStrictEffectiveLoader_ProductionLoadFileParity_RED(t *testing.T) {
	// Genuine RED observed 2026-07-21: config.LoadFile accepted unknown core fields,
	// empty documents, multi-doc, and trailing content. Keep this as the exact
	// future integration seam until task 2.1 wires LoadEffective.
	t.Skip("RED until task 2.1 wires shared LoadEffective/strict decode so LoadFile-equivalent startup rejects unknown core fields")
}

func TestStrictReloadSource_ReadStableIntegration_RED(t *testing.T) {
	t.Skip("RED until task 2.1 implements configsource.ReadStable with handle re-stat and atomic replacement enforcement")
}

func TestNoopEffectiveIdentity_CommentAndKeyOrder(t *testing.T) {
	t.Parallel()

	base := `
server:
  address: "127.0.0.1:0"
logging:
  level: info
  format: json
`
	withComments := `
# leading comment
server:
  address: "127.0.0.1:0" # inline
logging:
  format: json
  level: info
`

	cfgA, cat, err := strictDecodeContract([]byte(base))
	if err != nil || cat != configsource.CategoryOK {
		t.Fatalf("base: cat=%q err=%v", cat, err)
	}
	cfgB, cat, err := strictDecodeContract([]byte(withComments))
	if err != nil || cat != configsource.CategoryOK {
		t.Fatalf("comments: cat=%q err=%v", cat, err)
	}

	idA, err := computeEffectiveIdentity(cfgA)
	if err != nil {
		t.Fatal(err)
	}
	idB, err := computeEffectiveIdentity(cfgB)
	if err != nil {
		t.Fatal(err)
	}
	if idA.PrivateDigest != idB.PrivateDigest {
		t.Fatal("comment/key-order must be no-op for private effective identity")
	}
	if idA.PublicFingerprint != idB.PublicFingerprint {
		t.Fatal("comment/key-order must be no-op for public fingerprint")
	}
	if strings.Contains(idA.PublicFingerprint, "secret") {
		t.Fatal("fingerprint must stay secret-safe")
	}
}

func TestNoopEffectiveIdentity_SecretOnlyChange(t *testing.T) {
	t.Parallel()

	mk := func(secret string) *config.Config {
		raw := []byte(`
server:
  address: "127.0.0.1:0"
auth:
  local_api_keys:
    - key_id: k1
      principal_id: p1
      key: "` + secret + `"
`)
		cfg, cat, err := strictDecodeContract(raw)
		if err != nil || cat != configsource.CategoryOK {
			t.Fatalf("decode: cat=%q err=%v", cat, err)
		}
		return cfg
	}

	secretA := "super-secret-key-aaaa"
	secretB := "super-secret-key-bbbb"
	idA, err := computeEffectiveIdentity(mk(secretA))
	if err != nil {
		t.Fatal(err)
	}
	idB, err := computeEffectiveIdentity(mk(secretB))
	if err != nil {
		t.Fatal(err)
	}
	if idA.PrivateDigest == idB.PrivateDigest {
		t.Fatal("secret-only change must alter private identity")
	}
	if idA.PublicFingerprint == "" || idB.PublicFingerprint == "" {
		t.Fatal("public fingerprint required")
	}
	if strings.Contains(idA.PublicFingerprint, secretA) || strings.Contains(idB.PublicFingerprint, secretB) {
		t.Fatalf("public fingerprint leaked secret: %q / %q", idA.PublicFingerprint, idB.PublicFingerprint)
	}
	// Redacted public form should still distinguish key material presence via redaction token stability;
	// both secrets redact to the same token so public fingerprint may match — that is acceptable.
	// Private identity is the no-op authority for secret-bearing equality (req 3.7).
}

func TestEffectiveLoadFile_CharacterizationDefaults(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Address != "127.0.0.1:8080" {
		t.Fatalf("default listen: %q", cfg.Server.Address)
	}
	if cfg.Diagnostics.HealthPath != "/healthz" {
		t.Fatalf("health path: %q", cfg.Diagnostics.HealthPath)
	}
	if cfg.Routing.MaxAttempts != 3 {
		t.Fatalf("max attempts: %d", cfg.Routing.MaxAttempts)
	}
	if cfg.Logging.Level != "info" || cfg.Logging.Format != "json" {
		t.Fatalf("logging defaults: %+v", cfg.Logging)
	}
	if cfg.Auth.LocalAPIKeys == nil {
		t.Fatal("LocalAPIKeys must be non-nil empty slice after load")
	}
}
