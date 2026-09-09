package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func TestResolveAPIKey_SecretBundleTakesPrecedence(t *testing.T) {
	cfg := Config{APIKey: "yaml-key"}
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{"api_key": []byte("bundle-key")},
	}
	t.Setenv(EnvOpenCodeGoAPIKey, "env-key")

	key, err := ResolveAPIKey(FactoryKindGo, cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if key != "bundle-key" {
		t.Fatalf("key=%q want bundle-key", key)
	}
}

func TestResolveAPIKey_EnvExpansionInYAML(t *testing.T) {
	t.Setenv("MY_CUSTOM_OPENCODE_KEY", "expanded-secret")
	cfg := Config{APIKey: "${MY_CUSTOM_OPENCODE_KEY}"}

	key, err := ResolveAPIKey(FactoryKindGo, cfg, backendplugin.SecretBundle{})
	if err != nil {
		t.Fatal(err)
	}
	if key != "expanded-secret" {
		t.Fatalf("key=%q want expanded-secret", key)
	}
}

func TestResolveAPIKey_BearerPrefixStripped(t *testing.T) {
	t.Parallel()
	cfg := Config{APIKey: "Bearer sk-with-bearer"}

	key, err := ResolveAPIKey(FactoryKindGo, cfg, backendplugin.SecretBundle{})
	if err != nil {
		t.Fatal(err)
	}
	if key != "sk-with-bearer" {
		t.Fatalf("key=%q want sk-with-bearer", key)
	}
}

func TestResolveAPIKey_KindSpecificEnv(t *testing.T) {
	t.Setenv(EnvOpenCodeGoAPIKey, "go-env-key")
	t.Setenv(EnvOpenCodeZenAPIKey, "zen-env-key")
	t.Setenv(EnvOpenCodeAPIKey, "generic-env-key")

	goKey, err := ResolveAPIKey(FactoryKindGo, Config{}, backendplugin.SecretBundle{})
	if err != nil {
		t.Fatal(err)
	}
	if goKey != "go-env-key" {
		t.Fatalf("goKey=%q want go-env-key", goKey)
	}

	zenKey, err := ResolveAPIKey(FactoryKindZen, Config{}, backendplugin.SecretBundle{})
	if err != nil {
		t.Fatal(err)
	}
	if zenKey != "zen-env-key" {
		t.Fatalf("zenKey=%q want zen-env-key", zenKey)
	}
}

func TestResolveAPIKey_AuthJSONDiscovery(t *testing.T) {
	tmp := t.TempDir()
	authPath := filepath.Join(tmp, "auth.json")
	content := `{
		"opencode-go": {"type": "api", "key": "from-auth-json-go"},
		"opencode-zen": {"type": "api", "key": "from-auth-json-zen"},
		"opencode": {"type": "api", "key": "from-auth-json-generic"}
	}`
	if err := os.WriteFile(authPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// Ensure env vars do not overshadow auth.json in this test
	t.Setenv(EnvOpenCodeGoAPIKey, "")
	t.Setenv(EnvOpenCodeZenAPIKey, "")
	t.Setenv(EnvOpenCodeAPIKey, "")

	cfg := Config{AuthJSONPath: authPath}

	goKey, err := ResolveAPIKey(FactoryKindGo, cfg, backendplugin.SecretBundle{})
	if err != nil {
		t.Fatal(err)
	}
	if goKey != "from-auth-json-go" {
		t.Fatalf("goKey=%q want from-auth-json-go", goKey)
	}

	zenKey, err := ResolveAPIKey(FactoryKindZen, cfg, backendplugin.SecretBundle{})
	if err != nil {
		t.Fatal(err)
	}
	if zenKey != "from-auth-json-zen" {
		t.Fatalf("zenKey=%q want from-auth-json-zen", zenKey)
	}
}

func TestResolveAPIKey_GenericOpenCodeEnvFallback(t *testing.T) {
	t.Setenv(EnvOpenCodeGoAPIKey, "")
	t.Setenv(EnvOpenCodeZenAPIKey, "")
	t.Setenv(EnvOpenCodeAPIKey, "generic-fallback-key")

	cfg := Config{AuthJSONPath: "/nonexistent/path/auth.json"}
	key, err := ResolveAPIKey(FactoryKindGo, cfg, backendplugin.SecretBundle{})
	if err != nil {
		t.Fatal(err)
	}
	if key != "generic-fallback-key" {
		t.Fatalf("key=%q want generic-fallback-key", key)
	}
}

func TestResolveAPIKey_MissingFails(t *testing.T) {
	t.Setenv(EnvOpenCodeGoAPIKey, "")
	t.Setenv(EnvOpenCodeZenAPIKey, "")
	t.Setenv(EnvOpenCodeAPIKey, "")

	cfg := Config{AuthJSONPath: "/nonexistent/path/auth.json"}
	_, err := ResolveAPIKey(FactoryKindGo, cfg, backendplugin.SecretBundle{})
	if err == nil {
		t.Fatal("expected error when no credentials are found")
	}
}
