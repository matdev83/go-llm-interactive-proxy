package cursorsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/fakebridge"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSettingSources_EmptyDefaultExact(t *testing.T) {
	t.Parallel()
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	cfg, err := Normalize(Input{APIKey: "k", BridgeExecutable: mustExe(t)}, "")
	require.NoError(t, err)
	require.Empty(t, cfg.SettingSources)
	require.Equal(t, []SettingSource{}, cfg.SettingSources)
}

func TestSettingSources_ExplicitAllowedExact(t *testing.T) {
	t.Parallel()
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	want := []string{"project", "user", "team", "mdm", "plugins", "all"}
	cfg, err := Normalize(Input{
		APIKey:           "k",
		BridgeExecutable: mustExe(t),
		SettingSources:   want,
	}, "")
	require.NoError(t, err)
	got := make([]string, len(cfg.SettingSources))
	for i, s := range cfg.SettingSources {
		got[i] = string(s)
	}
	require.Equal(t, want, got)
}

func TestSettingSources_InvalidFailsBeforeProcess(t *testing.T) {
	t.Parallel()
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	_, err := Normalize(Input{
		APIKey:           "k",
		BridgeExecutable: mustExe(t),
		SettingSources:   []string{"ambient-home"},
	}, "")
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "setting")

	starter := &countingStarter{}
	cfg := openTestConfig(t, mustExe(t), t.TempDir())
	cfg.SandboxMode = SandboxOff
	cfg.SettingSources = []SettingSource{SettingSource("ambient-home")}
	rt := newBackendRuntime(cfg, runtimeOpts{Starter: starter, HostEnv: openTestHostEnv()})
	t.Cleanup(func() { _ = rt.Close() })
	acceptNatives(t, rt.tracking, "gpt-5.3-codex")
	_, err = rt.Open(context.Background(), textCall("gpt-5.3-codex"), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "setting")
	require.Zero(t, starter.starts.Load())
}

func TestMCP_NormalizedIdentityDeterministicOrderIndependent(t *testing.T) {
	t.Parallel()
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	a := json.RawMessage(`{"z":{"cmd":"a"},"a":{"cmd":"b"}}`)
	b := json.RawMessage(`{"a":{"cmd":"b"},"z":{"cmd":"a"}}`)
	cfgA, err := Normalize(Input{APIKey: "k", BridgeExecutable: mustExe(t), MCPServers: a}, "")
	require.NoError(t, err)
	cfgB, err := Normalize(Input{APIKey: "k", BridgeExecutable: mustExe(t), MCPServers: b}, "")
	require.NoError(t, err)
	require.True(t, json.Valid(cfgA.MCPServers))
	require.Equal(t, string(cfgA.MCPServers), string(cfgB.MCPServers))
	require.LessOrEqual(t, len(cfgA.MCPServers), MaxMCPConfigBytes)
	require.Equal(t, FingerprintJSON(cfgA.MCPServers), FingerprintJSON(cfgB.MCPServers))
	require.Equal(t, `{"a":{"cmd":"b"},"z":{"cmd":"a"}}`, string(cfgA.MCPServers))
}

func TestMCP_Exceeds256KiBRejected(t *testing.T) {
	t.Parallel()
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	huge := []byte(`{"pad":"` + strings.Repeat("x", MaxMCPConfigBytes) + `"}`)
	_, err := Normalize(Input{APIKey: "k", BridgeExecutable: mustExe(t), MCPServers: huge}, "")
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "mcp")
}

func TestMCP_InvalidJSONRejected(t *testing.T) {
	t.Parallel()
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	_, err := Normalize(Input{
		APIKey:           "k",
		BridgeExecutable: mustExe(t),
		MCPServers:       json.RawMessage(`not-json`),
	}, "")
	require.Error(t, err)
}

func TestMCP_BridgeReceivesNormalizedConfigOnly(t *testing.T) {
	exe := buildFakeBridgeExe(t)
	ws := t.TempDir()
	cfg := openTestConfig(t, exe, ws)
	cfg.SandboxMode = SandboxOff
	raw := json.RawMessage(`{"z":{"command":"echo"},"a":{"command":"true"}}`)
	norm, err := NormalizeMCPServers(raw)
	require.NoError(t, err)
	cfg.MCPServers = norm

	rt, capBridge := runtimeWithCreateCapture(t, cfg)
	t.Cleanup(func() { _ = rt.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	snap, err := rt.tracking.LoadModels(ctx)
	require.NoError(t, err)
	rt.tracking.AcceptInventory(snap.Models)

	stream, err := rt.Open(ctx, textCall("gpt-5.3-codex"), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()
	_ = drainManaged(ctx, t, stream)

	require.NotNil(t, capBridge.lastCreate)
	require.JSONEq(t, string(norm), string(capBridge.lastCreate.MCPServers))
	require.False(t, bytes.Equal(raw, capBridge.lastCreate.MCPServers), "bridge must receive normalized MCP bytes")
}

func TestMCP_AgentKeyFingerprintChangesWithSurface(t *testing.T) {
	t.Parallel()
	base := buildAgentKey(Config{
		APIKey:      "k",
		MCPServers:  json.RawMessage(`{"a":{}}`),
		SandboxMode: SandboxRequired,
	}, nil, "m", "/w", nil)
	other := buildAgentKey(Config{
		APIKey:      "k",
		MCPServers:  json.RawMessage(`{"b":{}}`),
		SandboxMode: SandboxRequired,
	}, nil, "m", "/w", nil)
	require.NotEqual(t, base.MCPFingerprint, other.MCPFingerprint)
	require.NotEqual(t, base.IdentityHash(), other.IdentityHash())
}

func TestSecurity_NoCustomToolsOrImplicitGoLIPMCP(t *testing.T) {
	cfg := openTestConfig(t, buildFakeBridgeExe(t), t.TempDir())
	cfg.SandboxMode = SandboxOff
	rt, capBridge := runtimeWithCreateCapture(t, cfg)
	t.Cleanup(func() { _ = rt.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	snap, err := rt.tracking.LoadModels(ctx)
	require.NoError(t, err)
	rt.tracking.AcceptInventory(snap.Models)
	stream, err := rt.Open(ctx, textCall("gpt-5.3-codex"), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()
	_ = drainManaged(ctx, t, stream)

	raw, err := json.Marshal(capBridge.lastCreate)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "customTools")
	require.NotContains(t, string(raw), "lipapi")
	require.NotContains(t, string(raw), "go-lip")
	require.NotContains(t, strings.ToLower(string(raw)), `"force"`)
}

func TestSandbox_RequiredDefault(t *testing.T) {
	t.Parallel()
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	cfg, err := Normalize(Input{APIKey: "k", BridgeExecutable: mustExe(t)}, "")
	require.NoError(t, err)
	require.Equal(t, SandboxRequired, cfg.SandboxMode)
	opts := sandboxOptionsFor(cfg.SandboxMode)
	require.NotNil(t, opts)
	require.True(t, opts.Enabled)
}

func TestSandbox_EmptyModeTreatedAsRequired(t *testing.T) {
	t.Parallel()
	require.Equal(t, SandboxRequired, EffectiveSandboxMode(""))
	require.True(t, sandboxOptionsFor("").Enabled)
	call := textCall("m")
	call.Session.AuthoritativeSessionID = "sess-fixed"
	keyEmpty := buildAgentKey(Config{APIKey: "k", SandboxMode: ""}, &call, "m", "/w", nil)
	keyReq := buildAgentKey(Config{APIKey: "k", SandboxMode: SandboxRequired}, &call, "m", "/w", nil)
	require.Equal(t, SandboxRequired, keyEmpty.Sandbox)
	require.Equal(t, keyReq.IdentityHash(), keyEmpty.IdentityHash())

	cfg := openTestConfig(t, mustExe(t), t.TempDir())
	cfg.SandboxMode = ""
	rt := newBackendRuntime(cfg, runtimeOpts{HostEnv: openTestHostEnv()})
	t.Cleanup(func() { _ = rt.Close() })
	require.Equal(t, SandboxRequired, rt.cfg.SandboxMode)
}

func TestSandbox_UnavailableFailsClosedWhenRequired(t *testing.T) {
	unsupported := false
	script := fakebridge.DefaultScript()
	script.SandboxSupported = &unsupported
	cfg := openTestConfig(t, buildFakeBridgeExe(t), t.TempDir())
	cfg.SandboxMode = SandboxRequired
	rt, capBridge := runtimeWithFakeScript(t, cfg, script)
	t.Cleanup(func() { _ = rt.Close() })

	acceptNatives(t, rt.tracking, "gpt-5.3-codex")
	_, err := rt.Open(context.Background(), textCall("gpt-5.3-codex"), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSandboxUnavailable)
	require.Contains(t, strings.ToLower(err.Error()), "sandbox")
	require.Nil(t, capBridge.lastCreate)
	require.Zero(t, rt.pool.LiveCount())
}

func TestSandbox_MissingInitializeFieldFailsClosed(t *testing.T) {
	script := fakebridge.DefaultScript()
	script.OmitSandboxSupported = true
	cfg := openTestConfig(t, buildFakeBridgeExe(t), t.TempDir())
	cfg.SandboxMode = ""
	rt, capBridge := runtimeWithFakeScript(t, cfg, script)
	t.Cleanup(func() { _ = rt.Close() })

	info, err := rt.agent.EnsureReady(context.Background())
	require.NoError(t, err)
	require.False(t, info.SandboxSupported, "missing initialize field must decode as false")

	acceptNatives(t, rt.tracking, "gpt-5.3-codex")
	_, err = rt.Open(context.Background(), textCall("gpt-5.3-codex"), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSandboxUnavailable)
	require.Nil(t, capBridge.lastCreate)
}

func TestSandbox_EnsureReadyDecodesSandboxSupportedFalse(t *testing.T) {
	unsupported := false
	script := fakebridge.DefaultScript()
	script.SandboxSupported = &unsupported
	cfg := openTestConfig(t, buildFakeBridgeExe(t), t.TempDir())
	rt, _ := runtimeWithFakeScript(t, cfg, script)
	t.Cleanup(func() { _ = rt.Close() })

	info, err := rt.agent.EnsureReady(context.Background())
	require.NoError(t, err)
	require.False(t, info.SandboxSupported)
}

func TestSandbox_ExplicitOffAllowedIncludingWindows(t *testing.T) {
	unsupported := false
	script := fakebridge.DefaultScript()
	script.SandboxSupported = &unsupported
	cfg := openTestConfig(t, buildFakeBridgeExe(t), t.TempDir())
	cfg.SandboxMode = SandboxOff
	rt, _ := runtimeWithFakeScript(t, cfg, script)
	t.Cleanup(func() { _ = rt.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	snap, err := rt.tracking.LoadModels(ctx)
	require.NoError(t, err)
	rt.tracking.AcceptInventory(snap.Models)
	stream, err := rt.Open(ctx, textCall("gpt-5.3-codex"), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()
	_ = drainManaged(ctx, t, stream)
	require.Equal(t, SandboxOff, rt.cfg.SandboxMode)
	_ = runtime.GOOS
}

func TestSandbox_RequiredNeverSilentDowngrade(t *testing.T) {
	unsupported := false
	script := fakebridge.DefaultScript()
	script.SandboxSupported = &unsupported
	cfg := openTestConfig(t, buildFakeBridgeExe(t), t.TempDir())
	cfg.SandboxMode = SandboxRequired
	rt, capBridge := runtimeWithFakeScript(t, cfg, script)
	t.Cleanup(func() { _ = rt.Close() })

	acceptNatives(t, rt.tracking, "gpt-5.3-codex")
	_, err := rt.Open(context.Background(), textCall("gpt-5.3-codex"), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.Error(t, err)
	require.Nil(t, capBridge.lastCreate, "must not create with sandbox silently disabled")
	require.NotContains(t, strings.Join(PlatformMinimumEnvNames(), ","), "FAKE_BRIDGE_SCRIPT")
}

func TestSecurity_AutoReviewIndependentDefaultFalse(t *testing.T) {
	t.Parallel()
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	cfg, err := Normalize(Input{APIKey: "k", BridgeExecutable: mustExe(t)}, "")
	require.NoError(t, err)
	require.False(t, cfg.AutoReview)

	cfgOn, err := Normalize(Input{APIKey: "k", BridgeExecutable: mustExe(t), AutoReview: true, SandboxMode: "off"}, "")
	require.NoError(t, err)
	require.True(t, cfgOn.AutoReview)
	require.Equal(t, SandboxOff, cfgOn.SandboxMode)
	require.NotEqual(t, cfg.AutoReview, cfgOn.AutoReview)
}

func TestSecurity_EnableAgentRetriesAlwaysFalse(t *testing.T) {
	cfg := openTestConfig(t, buildFakeBridgeExe(t), t.TempDir())
	cfg.SandboxMode = SandboxOff
	rt, capBridge := runtimeWithCreateCapture(t, cfg)
	t.Cleanup(func() { _ = rt.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	snap, err := rt.tracking.LoadModels(ctx)
	require.NoError(t, err)
	rt.tracking.AcceptInventory(snap.Models)
	stream, err := rt.Open(ctx, textCall("gpt-5.3-codex"), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()
	_ = drainManaged(ctx, t, stream)
	require.False(t, capBridge.lastCreate.EnableAgentRetries)
}

func TestEnvironment_AllowlistExactAPIKeyAbsent(t *testing.T) {
	t.Parallel()
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	cfg, err := Normalize(Input{APIKey: "secret-key-value", BridgeExecutable: mustExe(t)}, "")
	require.NoError(t, err)
	host := []string{
		"PATH=/bin",
		"CURSOR_API_KEY=" + cfg.APIKey,
		"HOME=/tmp",
		"OPENAI_API_KEY=other",
		"EXTRA=1",
	}
	if runtime.GOOS == "windows" {
		host = []string{
			"PATH=C:\\Windows\\System32",
			"SYSTEMROOT=C:\\Windows",
			"CURSOR_API_KEY=" + cfg.APIKey,
			"USERPROFILE=C:\\Users\\x",
			"EXTRA=1",
		}
	}
	selected := SelectHostEnv(host, cfg.BridgeEnvAllowlist)
	joined := strings.Join(selected, "\n")
	require.NotContains(t, joined, cfg.APIKey)
	require.NotContains(t, joined, "CURSOR_API_KEY")
	require.NotContains(t, joined, "OPENAI_API_KEY")
	require.NotContains(t, joined, "EXTRA=")
	for _, name := range PlatformMinimumEnvNames() {
		require.Contains(t, cfg.BridgeEnvAllowlist, name)
	}
}

func TestSecurity_NoLocalForce(t *testing.T) {
	t.Parallel()
	local := protocol.AgentCreateLocal{Cwd: "/tmp/ws"}
	raw, err := json.Marshal(local)
	require.NoError(t, err)
	require.JSONEq(t, `{"cwd":"/tmp/ws"}`, string(raw))
	require.NotContains(t, string(raw), "force")

	create := protocol.AgentCreateParams{
		APIKey:             "k",
		Model:              protocol.ModelSelection{ID: "m"},
		Local:              local,
		SettingSources:     []string{},
		SandboxOptions:     &protocol.SandboxOptions{Enabled: true},
		AutoReview:         false,
		EnableAgentRetries: false,
	}
	raw, err = json.Marshal(create)
	require.NoError(t, err)
	var obj map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &obj))
	require.NotContains(t, obj, "customTools")
	var localObj map[string]any
	require.NoError(t, json.Unmarshal(obj["local"], &localObj))
	require.NotContains(t, localObj, "force")
}

func TestSecurity_ConfigChangesInvalidateAgentKey(t *testing.T) {
	t.Parallel()
	baseCfg := Config{
		APIKey:         "k",
		SettingSources: []SettingSource{SettingSourceProject},
		MCPServers:     json.RawMessage(`{"a":{}}`),
		SandboxMode:    SandboxRequired,
		AutoReview:     false,
	}
	base := buildAgentKey(baseCfg, nil, "m", "/w", nil)
	h0 := base.IdentityHash()
	cases := []struct {
		name string
		mut  func(*Config)
	}{
		{"settings", func(c *Config) { c.SettingSources = []SettingSource{SettingSourceUser} }},
		{"mcp", func(c *Config) { c.MCPServers = json.RawMessage(`{"b":{}}`) }},
		{"sandbox", func(c *Config) { c.SandboxMode = SandboxOff }},
		{"autoreview", func(c *Config) { c.AutoReview = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := baseCfg
			tc.mut(&c)
			assert.NotEqual(t, h0, buildAgentKey(c, nil, "m", "/w", nil).IdentityHash())
		})
	}
}

func mustExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	return exe
}

func runtimeWithCreateCapture(t *testing.T, cfg Config) (*backendRuntime, *captureCreateBridge) {
	t.Helper()
	rt := newBackendRuntime(cfg, runtimeOpts{HostEnv: openTestHostEnv()})
	capBridge := &captureCreateBridge{inner: rt.agent}
	rt.agent = capBridge
	rt.pool = NewSessionPool(rt.cfg, capBridge, SessionPoolOpts{})
	return rt, capBridge
}

func runtimeWithFakeScript(t *testing.T, cfg Config, script fakebridge.Script) (*backendRuntime, *captureCreateBridge) {
	t.Helper()
	raw, err := json.Marshal(script)
	require.NoError(t, err)
	starter := &injectEnvStarter{
		inner: OSProcessStarter{},
		extra: []string{"FAKE_BRIDGE_SCRIPT=" + string(raw)},
	}
	rt := newBackendRuntime(cfg, runtimeOpts{Starter: starter, HostEnv: openTestHostEnv()})
	capBridge := &captureCreateBridge{inner: rt.agent}
	rt.agent = capBridge
	rt.pool = NewSessionPool(rt.cfg, capBridge, SessionPoolOpts{})
	return rt, capBridge
}

type injectEnvStarter struct {
	inner ProcessStarter
	extra []string
}

func (s *injectEnvStarter) Start(cmd []string, cwd string, env []string) (Process, error) {
	merged := append(append([]string{}, env...), s.extra...)
	return s.inner.Start(cmd, cwd, merged)
}

type countingStarter struct {
	starts atomic.Int32
}

func (c *countingStarter) Start(cmd []string, cwd string, env []string) (Process, error) {
	c.starts.Add(1)
	return nil, context.Canceled
}

type captureCreateBridge struct {
	inner      AgentBridge
	mu         sync.Mutex
	lastCreate *protocol.AgentCreateParams
}

func (c *captureCreateBridge) Generation() int64 { return c.inner.Generation() }
func (c *captureCreateBridge) EnsureReady(ctx context.Context) (BridgeInfo, error) {
	return c.inner.EnsureReady(ctx)
}

func (c *captureCreateBridge) CreateAgent(ctx context.Context, params protocol.AgentCreateParams) (string, error) {
	c.mu.Lock()
	cp := params
	c.lastCreate = &cp
	c.mu.Unlock()
	return c.inner.CreateAgent(ctx, params)
}

func (c *captureCreateBridge) SendAgent(ctx context.Context, agentID, prompt string) (string, error) {
	return c.inner.SendAgent(ctx, agentID, prompt)
}

func (c *captureCreateBridge) DisposeAgent(ctx context.Context, agentID string) error {
	return c.inner.DisposeAgent(ctx, agentID)
}

func (c *captureCreateBridge) SubscribeRun(runID string) (<-chan *protocol.Frame, func(), func() error) {
	return c.inner.SubscribeRun(runID)
}

func (c *captureCreateBridge) CancelRun(ctx context.Context, runID string) error {
	return c.inner.CancelRun(ctx, runID)
}
