package product

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentKey_FingerprintExcludesRawSecretAndWorkspacePath(t *testing.T) {
	t.Parallel()
	raw := "crsr_super_secret_key_value"
	ws := `/home/user/secret-project`
	fp := FingerprintSecret(raw)
	require.NotEqual(t, raw, fp)
	key := AgentKey{
		SessionID:           "sess-1",
		Workspace:           ws,
		ModelID:             "gpt-test",
		KeyFingerprint:      fp,
		SettingsFingerprint: FingerprintSettingSources([]SettingSource{SettingSourceProject}),
		MCPFingerprint:      FingerprintJSON([]byte(`{"a":1}`)),
		Sandbox:             SandboxRequired,
	}
	diag := key.DiagnosticString()
	assert.NotContains(t, diag, raw)
	assert.NotContains(t, diag, ws)
	assert.NotContains(t, diag, "secret-project")
	assert.NotContains(t, diag, "prompt")
	assert.Contains(t, diag, "workspacefp=")
	assert.Contains(t, diag, "keyfp=")
	assert.True(t, strings.HasPrefix(strings.Split(diag, "keyfp=")[1], fp[:12]))
}

func TestAgentKey_IdentityChangesWithModelWorkspaceKeySettingsMCPSafety(t *testing.T) {
	t.Parallel()
	base := AgentKey{
		SessionID:              "s",
		Workspace:              "/a",
		ModelID:                "m1",
		ModelParamsFingerprint: FingerprintJSON([]byte(`[{"id":"reasoning","value":"high"}]`)),
		KeyFingerprint:         FingerprintSecret("k1"),
		SettingsFingerprint:    FingerprintSettingSources([]SettingSource{SettingSourceUser}),
		MCPFingerprint:         FingerprintJSON([]byte(`{"srv":{}}`)),
		Sandbox:                SandboxRequired,
		AutoReview:             false,
	}
	h0 := base.IdentityHash()
	cases := []struct {
		name string
		mut  func(*AgentKey)
	}{
		{"model", func(k *AgentKey) { k.ModelID = "m2" }},
		{"workspace", func(k *AgentKey) { k.Workspace = "/b" }},
		{"key", func(k *AgentKey) { k.KeyFingerprint = FingerprintSecret("k2") }},
		{"settings", func(k *AgentKey) {
			k.SettingsFingerprint = FingerprintSettingSources([]SettingSource{SettingSourceTeam})
		}},
		{"mcp", func(k *AgentKey) { k.MCPFingerprint = FingerprintJSON([]byte(`{"other":{}}`)) }},
		{"sandbox", func(k *AgentKey) { k.Sandbox = SandboxOff }},
		{"autoReview", func(k *AgentKey) { k.AutoReview = true }},
		{"modelParams", func(k *AgentKey) {
			k.ModelParamsFingerprint = FingerprintJSON([]byte(`[{"id":"reasoning","value":"low"}]`))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := base
			tc.mut(&k)
			assert.NotEqual(t, h0, k.IdentityHash())
		})
	}
}

func TestPlanHistory_BootstrapIncrementalRetryAndDivergence(t *testing.T) {
	t.Parallel()
	key := AgentKey{SessionID: "s", Workspace: "/w", ModelID: "m", KeyFingerprint: FingerprintSecret("k")}
	gen := int64(3)

	boot := PlanHistory(TranscriptView{MessageCount: 2, PrefixHash: "h2", HeadPrefixHash: "", LastTurnID: "t2"}, HistoryMarker{}, key, gen)
	require.Equal(t, HistoryBootstrap, boot.Mode)
	require.True(t, boot.UseFullPrompt)

	committed := boot.NextMarker
	inc := PlanHistory(TranscriptView{MessageCount: 3, PrefixHash: "h3", HeadPrefixHash: "h2", LastTurnID: "t3"}, committed, key, gen)
	require.Equal(t, HistoryIncremental, inc.Mode)
	require.False(t, inc.ResetNeeded)

	retry := PlanHistory(TranscriptView{MessageCount: 3, PrefixHash: "h3", HeadPrefixHash: "h3", LastTurnID: "t3"}, inc.NextMarker, key, gen)
	require.Equal(t, HistoryRetry, retry.Mode)
}

func TestPlanHistory_FailClosedOnEditReorderMissingHeadHashTruncation(t *testing.T) {
	t.Parallel()
	key := AgentKey{SessionID: "s", Workspace: "/w", ModelID: "m", KeyFingerprint: FingerprintSecret("k")}
	gen := int64(1)
	committed := HistoryMarker{
		MessageCount: 3, PrefixHash: "h3", LastTurnID: "t3",
		AgentIdentityHash: key.IdentityHash(), ProcessGeneration: gen,
	}

	cases := []struct {
		name string
		view TranscriptView
	}{
		{"same_length_edit", TranscriptView{MessageCount: 3, PrefixHash: "h3edit", HeadPrefixHash: "h3edit", LastTurnID: "t3"}},
		{"same_length_reorder", TranscriptView{MessageCount: 3, PrefixHash: "h3re", HeadPrefixHash: "h3re", LastTurnID: "t3"}},
		{"wrong_head_hash", TranscriptView{MessageCount: 4, PrefixHash: "h4", HeadPrefixHash: "WRONG", LastTurnID: "t4"}},
		{"missing_head_hash", TranscriptView{MessageCount: 4, PrefixHash: "h4", HeadPrefixHash: "", LastTurnID: "t4"}},
		{"truncation", TranscriptView{MessageCount: 1, PrefixHash: "h1", HeadPrefixHash: "h1", LastTurnID: "t1"}},
		{"inconsistent_full_hash_same_count", TranscriptView{MessageCount: 3, PrefixHash: "hOTHER", HeadPrefixHash: "h3", LastTurnID: "t3"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := PlanHistory(tc.view, committed, key, gen)
			require.True(t, plan.ResetNeeded, tc.name)
			require.Equal(t, HistoryBootstrap, plan.Mode)
			require.True(t, plan.UseFullPrompt)
		})
	}

	appendOK := PlanHistory(TranscriptView{MessageCount: 4, PrefixHash: "h4", HeadPrefixHash: "h3", LastTurnID: "t4"}, committed, key, gen)
	require.Equal(t, HistoryIncremental, appendOK.Mode)
	require.False(t, appendOK.ResetNeeded)
}
