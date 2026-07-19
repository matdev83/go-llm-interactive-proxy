package cursorsdk

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// agentKeyFingerprintMACKey domain-separates deterministic identity fingerprints.
// This is not password hashing: values are cache/pool identity digests, never
// stored credentials or authentication verifiers.
var agentKeyFingerprintMACKey = []byte("lip/cursorsdk/agent-key-fingerprint/v1")

type AgentKey struct {
	SessionID              string
	Workspace              string
	ModelID                string
	ModelParamsFingerprint string
	KeyFingerprint         string
	SettingsFingerprint    string
	MCPFingerprint         string
	Sandbox                SandboxMode
	AutoReview             bool
}

func resolveAgentSessionID(call *lipapi.Call) string {
	if call != nil {
		if auth := strings.TrimSpace(call.Session.AuthoritativeSessionID); auth != "" {
			return auth
		}
		if id := strings.TrimSpace(call.ID); id != "" {
			return "attempt:" + id
		}
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		sum := sha256.Sum256(fmt.Appendf(nil, "%p", call))
		return "attempt:" + hex.EncodeToString(sum[:8])
	}
	return "attempt:" + hex.EncodeToString(b[:])
}

func buildAgentKey(cfg Config, call *lipapi.Call, native, workspace string, modelParams json.RawMessage) AgentKey {
	return AgentKey{
		SessionID:              resolveAgentSessionID(call),
		Workspace:              workspace,
		ModelID:                native,
		ModelParamsFingerprint: FingerprintModelParams(modelParams),
		KeyFingerprint:         FingerprintSecret(cfg.APIKey),
		SettingsFingerprint:    FingerprintSettingSources(cfg.SettingSources),
		MCPFingerprint:         FingerprintJSON(cfg.MCPServers),
		Sandbox:                EffectiveSandboxMode(cfg.SandboxMode),
		AutoReview:             cfg.AutoReview,
	}
}

// FingerprintSecret returns a deterministic HMAC-SHA256 hex digest for agent
// pool / diagnostic identity. It must not be used as a password hash or as a
// credential verifier.
func FingerprintSecret(secret string) string {
	mac := hmac.New(sha256.New, agentKeyFingerprintMACKey)
	_, _ = mac.Write([]byte(secret))
	return hex.EncodeToString(mac.Sum(nil))
}

func FingerprintJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return FingerprintSecret("")
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return FingerprintSecret(string(raw))
	}
	canon, err := json.Marshal(normalized)
	if err != nil {
		return FingerprintSecret(string(raw))
	}
	return FingerprintSecret(string(canon))
}

func FingerprintSettingSources(sources []SettingSource) string {
	parts := make([]string, len(sources))
	for i, s := range sources {
		parts[i] = string(s)
	}
	return FingerprintSecret(strings.Join(parts, ","))
}

func FingerprintModelParams(raw json.RawMessage) string {
	return FingerprintJSON(raw)
}

func (k AgentKey) IdentityHash() string {
	material := strings.Join([]string{
		k.SessionID,
		k.Workspace,
		k.ModelID,
		k.ModelParamsFingerprint,
		k.KeyFingerprint,
		k.SettingsFingerprint,
		k.MCPFingerprint,
		string(k.Sandbox),
		fmt.Sprintf("%t", k.AutoReview),
	}, "\x1f")
	return FingerprintSecret(material)
}

func (k AgentKey) DiagnosticString() string {
	return fmt.Sprintf(
		"session=%s workspacefp=%s model=%s keyfp=%s settings=%s mcp=%s sandbox=%s autoreview=%t id=%s",
		k.SessionID,
		truncateFP(FingerprintSecret(k.Workspace)),
		k.ModelID,
		truncateFP(k.KeyFingerprint),
		truncateFP(k.SettingsFingerprint),
		truncateFP(k.MCPFingerprint),
		k.Sandbox,
		k.AutoReview,
		truncateFP(k.IdentityHash()),
	)
}

func truncateFP(fp string) string {
	if len(fp) <= 12 {
		return fp
	}
	return fp[:12]
}
