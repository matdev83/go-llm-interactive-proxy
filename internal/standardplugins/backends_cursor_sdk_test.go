package standardplugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"gopkg.in/yaml.v3"
)

func cursorSDKTestYAML(t *testing.T, extra string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return "api_key: yaml-cursor-secret\nbridge_executable: " + yamlQuote(exe) + "\n" + extra
}

func yamlQuote(s string) string {
	b, _ := yaml.Marshal(s)
	return strings.TrimSpace(string(b))
}

func decodeCursorSDKNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestResolveUpstreamAPIKeysFromEnv_cursorAPIKey(t *testing.T) {
	clearAllProviderEnv(t)
	t.Setenv("CURSOR_API_KEY", "cursor-env-secret")
	t.Setenv("CURSOR_API_KEY_2", "must-not-be-read")
	got := ResolveUpstreamAPIKeysFromEnv()
	if got.Cursor != "cursor-env-secret" {
		t.Fatalf("Cursor = %q, want cursor-env-secret", got.Cursor)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_cursorAPIKeyEmpty(t *testing.T) {
	clearAllProviderEnv(t)
	t.Setenv("CURSOR_API_KEY", "")
	got := ResolveUpstreamAPIKeysFromEnv()
	if got.Cursor != "" {
		t.Fatalf("Cursor = %q, want empty", got.Cursor)
	}
}

func TestBackendCursorSDK_envKeyFallbackBound(t *testing.T) {
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw := "bridge_executable: " + yamlQuote(exe) + "\n"
	sc, _, err := parseCursorSDKScaffold(decodeCursorSDKNode(t, raw), UpstreamAPIKeys{Cursor: "env-only-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if !sc.APIKeyEquals("env-only-secret") {
		t.Fatal("expected scaffold to bind CURSOR_API_KEY fallback")
	}
	if sc.APIKeyEquals("yaml-cursor-secret") {
		t.Fatal("env fallback must not equal yaml key")
	}
	be := sc.Backend()
	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != cursorsdk.ID {
		t.Fatalf("BackendPrefixes = %v, want [%s]", be.BackendPrefixes, cursorsdk.ID)
	}
}

func TestBackendCursorSDK_yamlKeyPrecedenceBound(t *testing.T) {
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	sc, _, err := parseCursorSDKScaffold(decodeCursorSDKNode(t, cursorSDKTestYAML(t, "")), UpstreamAPIKeys{Cursor: "env-should-lose"})
	if err != nil {
		t.Fatal(err)
	}
	if !sc.APIKeyEquals("yaml-cursor-secret") {
		t.Fatal("expected scaffold to bind explicit yaml api_key")
	}
	if sc.APIKeyEquals("env-should-lose") {
		t.Fatal("yaml api_key must take precedence over env")
	}
	if sc.SandboxMode() != cursorsdk.SandboxRequired {
		t.Fatalf("SandboxMode = %q, want required", sc.SandboxMode())
	}
	if len(sc.SettingSources()) != 0 {
		t.Fatalf("SettingSources = %v, want empty default", sc.SettingSources())
	}
}

func TestBackendCursorSDK_missingKeySecretSafe(t *testing.T) {
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	secret := "leaky-secret-value-xyz"
	raw := "bridge_executable: " + yamlQuote(exe) + "\n"
	_, _, err = parseCursorSDKScaffold(decodeCursorSDKNode(t, raw), UpstreamAPIKeys{})
	if err == nil {
		t.Fatal("expected missing api_key error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked secret material: %v", err)
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("error = %v, want api_key mention", err)
	}
}

func TestSecretSafeCursorSDKErr_redactsInjectedSecret(t *testing.T) {
	t.Parallel()
	secret := "injected-secret-value-abc"
	err := secretSafeCursorSDKErr(fmt.Errorf("boom api_key=%s trailing", secret), secret)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("redaction failed: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("error = %v, want [redacted]", err)
	}
}

func TestBackendCursorSDK_unknownKeysRejected(t *testing.T) {
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	_, _, err := parseCursorSDKScaffold(decodeCursorSDKNode(t, cursorSDKTestYAML(t, "npm_install: true\n")), UpstreamAPIKeys{})
	if err == nil {
		t.Fatal("expected unknown key error")
	}
	if !strings.Contains(err.Error(), "unknown config key") {
		t.Fatalf("error = %v, want unknown config key", err)
	}
	if strings.Contains(err.Error(), "yaml-cursor-secret") {
		t.Fatalf("error leaked api_key: %v", err)
	}
}

func TestBackendCursorSDK_rejectsCloudRuntimeKey(t *testing.T) {
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	_, _, err := parseCursorSDKScaffold(decodeCursorSDKNode(t, cursorSDKTestYAML(t, "cloud: true\n")), UpstreamAPIKeys{})
	if err == nil {
		t.Fatal("expected unknown key error for cloud")
	}
	if !strings.Contains(err.Error(), "unknown config key") {
		t.Fatalf("error = %v, want unknown config key", err)
	}
}

func TestBackendCursorSDK_bridgeExecutableLookup(t *testing.T) {
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	missing := filepath.Join(t.TempDir(), "no-such-bridge")
	raw := "api_key: yaml-cursor-secret\nbridge_executable: " + yamlQuote(missing) + "\n"
	_, _, err := parseCursorSDKScaffold(decodeCursorSDKNode(t, raw), UpstreamAPIKeys{})
	if err == nil {
		t.Fatal("expected missing bridge executable error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not found") {
		t.Fatalf("error = %v, want not found", err)
	}
	if strings.Contains(err.Error(), "yaml-cursor-secret") {
		t.Fatalf("error leaked api_key: %v", err)
	}
}

func TestBackendCursorSDK_openOperationalRejectsUnknownModel(t *testing.T) {
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	sc, _, err := parseCursorSDKScaffold(decodeCursorSDKNode(t, cursorSDKTestYAML(t, "")), UpstreamAPIKeys{})
	if err != nil {
		t.Fatal(err)
	}
	be := sc.Backend()
	t.Cleanup(func() { _ = be.Close() })
	if be.Open == nil || be.Close == nil || be.ModelInventory == nil || be.ResolveCaps == nil {
		t.Fatal("expected operational backend surface")
	}
	if be.EnforcesMaxOutputTokens {
		t.Fatal("cursorsdk must not claim max-output enforcement")
	}
	call := lipapi.Call{
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIResponses,
			DeliveryMode:  lipapi.DeliveryModeStreaming,
			TransportMode: lipapi.TransportModeStreaming,
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, err = be.Open(context.Background(), call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "missing-model"},
	})
	if err == nil {
		t.Fatal("expected Open error for unknown/unaccepted model")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected cancel: %v", err)
	}
	if strings.Contains(err.Error(), "yaml-cursor-secret") {
		t.Fatalf("Open error leaked api_key: %v", err)
	}
	if strings.Contains(err.Error(), "backend runtime construction is not implemented") {
		t.Fatalf("Open still phase-blocked: %v", err)
	}
	if !sc.HasAPIKey() {
		t.Fatal("operational Open must still retain bound API key in scaffold")
	}
}

func TestStandardBackendBundle_excludesCursorSDK_optInRegistration(t *testing.T) {
	t.Parallel()
	for _, e := range StandardBackendBundle(UpstreamAPIKeys{}).Backends {
		if e.ID == cursorsdk.ID {
			t.Fatal("cursorsdk must not be in StandardBackendBundle (essential-only; use ExperimentalCursorSDKRegistration)")
		}
	}
	reg := pluginreg.NewRegistry()
	if err := InstallBundleOn(reg, Bundle{Backends: []BackendRegistration{
		ExperimentalCursorSDKRegistration(UpstreamAPIKeys{}),
	}}); err != nil {
		t.Fatal(err)
	}
	p, ok := reg.BackendSecurityProfile(cursorsdk.ID)
	if !ok {
		t.Fatal("missing security profile for opt-in cursorsdk")
	}
	if p.CredentialMode != pluginreg.CredentialStatic {
		t.Fatalf("CredentialMode = %q, want CredentialStatic", p.CredentialMode)
	}
	if p.AccessScope != pluginreg.BackendAccessLocalOnly {
		t.Fatalf("AccessScope = %q, want BackendAccessLocalOnly", p.AccessScope)
	}
}

func TestStandardDistributionRequirements_excludesCursorSDK(t *testing.T) {
	t.Parallel()
	for _, req := range lipsdk.StandardDistributionRequirements() {
		if req.ID == cursorsdk.ID {
			t.Fatal("cursorsdk must not be in StandardDistributionRequirements")
		}
	}
}

func TestBackendCursorSDK_platformEnvAllowlistBound(t *testing.T) {
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	sc, _, err := parseCursorSDKScaffold(decodeCursorSDKNode(t, cursorSDKTestYAML(t, "")), UpstreamAPIKeys{})
	if err != nil {
		t.Fatal(err)
	}
	allow := sc.EnvAllowlist()
	for _, name := range cursorsdk.PlatformMinimumEnvNames() {
		if !slices.Contains(allow, name) {
			t.Fatalf("EnvAllowlist missing platform minimum %q on %s: %v", name, runtime.GOOS, allow)
		}
	}
}

func TestBackendCursorSDK_staticModelsOverride(t *testing.T) {
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	raw := cursorSDKTestYAML(t, `models:
  source: inline
  items:
    - canonical_id: cursor/composer-2-fast
      native_id: composer-2-fast
      display_name: Composer 2 Fast
`)
	be, err := backendCursorSDK(decodeCursorSDKNode(t, raw), nil, UpstreamAPIKeys{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := be.ModelInventory.(*acp.TrackingInventory); !ok {
		t.Fatalf("ModelInventory type = %T, want *acp.TrackingInventory", be.ModelInventory)
	}
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if len(snap.Models) != 1 {
		t.Fatalf("models = %+v, want 1", snap.Models)
	}
	if snap.Models[0].CanonicalID != "cursor/composer-2-fast" || snap.Models[0].NativeID != "composer-2-fast" {
		t.Fatalf("model = %+v", snap.Models[0])
	}
}
