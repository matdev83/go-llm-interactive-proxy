package product_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/require"
)

func testBridgeExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	return exe
}

func baseInput(t *testing.T) product.Input {
	t.Helper()
	return product.Input{
		APIKey:           "secret-cursor-key-value",
		BridgeExecutable: testBridgeExe(t),
	}
}

func TestNormalize_defaults(t *testing.T) {
	t.Parallel()
	product.ResetLookPathCache()
	t.Cleanup(product.ResetLookPathCache)

	cfg, err := product.Normalize(baseInput(t), "")
	require.NoError(t, err)
	require.Equal(t, 32, cfg.MaxAgents)
	require.Equal(t, 8, cfg.MaxConcurrentRuns)
	require.Equal(t, 30*time.Second, cfg.BridgeStartTimeout)
	require.Equal(t, 5*time.Second, cfg.CancelTimeout)
	require.Equal(t, 10*time.Second, cfg.ShutdownTimeout)
	require.Equal(t, 15*time.Minute, cfg.AgentIdleTimeout)
	require.Empty(t, cfg.SettingSources)
	require.Equal(t, product.SandboxRequired, cfg.SandboxMode)
	require.False(t, cfg.AutoReview)
	require.Equal(t, product.MaxFrameBytes, 16<<20)
	require.Equal(t, product.MaxPromptBytes, 8<<20)
	require.Equal(t, product.MaxMCPConfigBytes, 256<<10)
	require.Equal(t, product.MaxStderrRetainBytes, 8<<10)
	for _, name := range product.PlatformMinimumEnvNames() {
		require.Contains(t, cfg.BridgeEnvAllowlist, name)
	}
}

func TestNormalize_apiKeyFallbackAndPrecedence(t *testing.T) {
	t.Parallel()
	product.ResetLookPathCache()
	t.Cleanup(product.ResetLookPathCache)

	in := baseInput(t)
	in.APIKey = ""
	cfg, err := product.Normalize(in, "env-cursor-key")
	require.NoError(t, err)
	require.Equal(t, "env-cursor-key", cfg.APIKey)

	in.APIKey = "yaml-wins"
	cfg, err = product.Normalize(in, "env-cursor-key")
	require.NoError(t, err)
	require.Equal(t, "yaml-wins", cfg.APIKey)
}

func TestNormalize_errorPathsNeverEchoAPIKey(t *testing.T) {
	t.Parallel()
	product.ResetLookPathCache()
	t.Cleanup(product.ResetLookPathCache)

	secret := "super-secret-should-not-leak"
	in := baseInput(t)
	in.APIKey = secret
	in.BridgeExecutable = filepath.Join(t.TempDir(), "missing-lip-cursor-sdk-bridge")
	_, err := product.Normalize(in, "")
	require.Error(t, err)
	require.NotContains(t, err.Error(), secret)
}

func TestNormalize_sdkSettingSourcesPinned(t *testing.T) {
	t.Parallel()
	product.ResetLookPathCache()
	t.Cleanup(product.ResetLookPathCache)

	raw, err := os.ReadFile("testdata/fixtures/sdk_setting_sources_1.0.23.txt")
	require.NoError(t, err)
	var want []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		want = append(want, line)
	}
	require.Equal(t, []string{"project", "user", "team", "mdm", "plugins", "all"}, want)

	in := baseInput(t)
	in.SettingSources = want
	cfg, err := product.Normalize(in, "")
	require.NoError(t, err)
	require.Len(t, cfg.SettingSources, len(want))

	in.SettingSources = []string{"ambient-home"}
	_, err = product.Normalize(in, "")
	require.Error(t, err)
}

func TestNormalize_bounds(t *testing.T) {
	t.Parallel()
	product.ResetLookPathCache()
	t.Cleanup(product.ResetLookPathCache)

	cases := []struct {
		name string
		mut  func(*product.Input)
	}{
		{"max_agents_low", func(in *product.Input) { v := 0; in.MaxAgents = &v }},
		{"max_agents_high", func(in *product.Input) { v := 33; in.MaxAgents = &v }},
		{"max_runs_high", func(in *product.Input) { v := 9; in.MaxConcurrentRuns = &v }},
		{"runs_gt_agents", func(in *product.Input) {
			a, r := 4, 5
			in.MaxAgents = &a
			in.MaxConcurrentRuns = &r
		}},
		{"start_low", func(in *product.Input) { v := 0.5; in.BridgeStartTimeoutSeconds = &v }},
		{"start_high", func(in *product.Input) { v := 121.0; in.BridgeStartTimeoutSeconds = &v }},
		{"cancel_low", func(in *product.Input) { v := 0.05; in.CancelTimeoutSeconds = &v }},
		{"cancel_high", func(in *product.Input) { v := 31.0; in.CancelTimeoutSeconds = &v }},
		{"shutdown_low", func(in *product.Input) { v := 0.5; in.ShutdownTimeoutSeconds = &v }},
		{"shutdown_high", func(in *product.Input) { v := 121.0; in.ShutdownTimeoutSeconds = &v }},
		{"idle_low", func(in *product.Input) { v := 0.5; in.AgentIdleTimeoutSeconds = &v }},
		{"idle_high", func(in *product.Input) {
			v := float64((24*time.Hour + time.Second) / time.Second)
			in.AgentIdleTimeoutSeconds = &v
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := baseInput(t)
			tc.mut(&in)
			_, err := product.Normalize(in, "")
			require.Error(t, err)
			require.NotContains(t, err.Error(), in.APIKey)
		})
	}
}

func TestNormalize_idleZeroDisables(t *testing.T) {
	t.Parallel()
	product.ResetLookPathCache()
	t.Cleanup(product.ResetLookPathCache)

	in := baseInput(t)
	zero := 0.0
	in.AgentIdleTimeoutSeconds = &zero
	cfg, err := product.Normalize(in, "")
	require.NoError(t, err)
	require.Equal(t, time.Duration(0), cfg.AgentIdleTimeout)
}

func TestNormalize_missingBridgeExecutable(t *testing.T) {
	t.Parallel()
	product.ResetLookPathCache()
	t.Cleanup(product.ResetLookPathCache)

	in := baseInput(t)
	in.BridgeExecutable = filepath.Join(t.TempDir(), "missing-lip-cursor-sdk-bridge")
	_, err := product.Normalize(in, "")
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "not found")
	require.Contains(t, err.Error(), "npm install")
	require.NotContains(t, err.Error(), in.APIKey)
}

func TestNormalize_rejectsShellAndNPMLaunchers(t *testing.T) {
	t.Parallel()
	product.ResetLookPathCache()
	t.Cleanup(product.ResetLookPathCache)

	for _, name := range []string{"npm", "npx", "bash", "cmd.exe", "powershell"} {
		in := baseInput(t)
		in.BridgeExecutable = name
		_, err := product.Normalize(in, "")
		require.Error(t, err, name)
		require.NotContains(t, err.Error(), in.APIKey)
	}
}

func TestNormalize_windowsAcceptsRequiredSandboxAtConfig(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("Windows parse semantics")
	}
	product.ResetLookPathCache()
	t.Cleanup(product.ResetLookPathCache)

	in := product.Input{
		APIKey:           "secret-cursor-key-value",
		BridgeExecutable: testBridgeExe(t),
		SandboxMode:      "",
	}
	cfg, err := product.Normalize(in, "")
	require.NoError(t, err)
	require.Equal(t, product.SandboxRequired, cfg.SandboxMode)
}

func TestNormalize_rejectsCredentialEnvAllowlist(t *testing.T) {
	t.Parallel()
	product.ResetLookPathCache()
	t.Cleanup(product.ResetLookPathCache)

	in := baseInput(t)
	in.BridgeEnvAllowlist = []string{"CURSOR_API_KEY"}
	_, err := product.Normalize(in, "")
	require.Error(t, err)
}

func TestConfig_argvAndSelectHostEnvNeverCarryAPIKey(t *testing.T) {
	t.Parallel()
	product.ResetLookPathCache()
	t.Cleanup(product.ResetLookPathCache)

	cfg, err := product.Normalize(baseInput(t), "")
	require.NoError(t, err)
	argv := cfg.BridgeArgv()
	require.Len(t, argv, 1)
	require.Equal(t, cfg.BridgeExecutable, argv[0])
	require.NotContains(t, argv[0], cfg.APIKey)

	host := []string{
		"PATH=/usr/bin",
		"CURSOR_API_KEY=" + cfg.APIKey,
		"HOME=/tmp",
		"OPENAI_API_KEY=other-secret",
	}
	if runtime.GOOS == "windows" {
		host = []string{
			"PATH=C:\\Windows\\System32",
			"SYSTEMROOT=C:\\Windows",
			"CURSOR_API_KEY=" + cfg.APIKey,
			"USERPROFILE=C:\\Users\\x",
		}
	}
	selected := product.SelectHostEnv(host, cfg.BridgeEnvAllowlist)
	joined := strings.Join(selected, "\n")
	require.NotContains(t, joined, cfg.APIKey)
	require.NotContains(t, joined, "CURSOR_API_KEY")
	require.NotContains(t, joined, "OPENAI_API_KEY")
	require.NotEmpty(t, selected)
}

func TestScaffold_retainsNormalizedConfig(t *testing.T) {
	t.Parallel()
	product.ResetLookPathCache()
	t.Cleanup(product.ResetLookPathCache)

	cfg, err := product.Normalize(baseInput(t), "")
	require.NoError(t, err)
	sc := product.NewScaffold(cfg)
	require.True(t, sc.APIKeyEquals("secret-cursor-key-value"))
	require.False(t, sc.APIKeyEquals("wrong"))
	require.Equal(t, cfg.BridgeExecutable, sc.BridgeExecutable())
	require.Equal(t, product.SandboxRequired, sc.SandboxMode())
	require.Equal(t, 32, sc.MaxAgents())
	sans := sc.ConfigSansSecret()
	require.Empty(t, sans.APIKey)
	require.Equal(t, cfg.BridgeExecutable, sans.BridgeExecutable)

	be := sc.Backend()
	require.NotNil(t, be.Open)
	require.NotNil(t, be.Close)
	call := lipapi.Call{
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIResponses,
			DeliveryMode:  lipapi.DeliveryModeStreaming,
			TransportMode: lipapi.TransportModeStreaming,
		},
	}
	_, err = be.Open(context.Background(), call, product.AttemptCandidate{})
	require.Error(t, err)
	require.NotContains(t, err.Error(), cfg.APIKey)
	require.NotContains(t, err.Error(), "backend runtime construction is not implemented")
	require.NoError(t, be.Close())
}
