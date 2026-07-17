package secretsguard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"gopkg.in/yaml.v3"
)

// RuntimeConfig is the decoded secrets-guard feature config used by runtimebundle.
type RuntimeConfig struct {
	Enabled               bool
	Action                string
	AuditFailurePolicy    string
	AuditConfigVersion    string
	IncludePopularEnv     bool
	IncludeEnv            []string
	ExcludeEnv            []string
	MinSecretBytes        int
	PreserveKnownPrefixes bool
	MaskByte              byte
}

// ComposeRuntimeConfig decodes enabled secrets-guard feature YAML.
func ComposeRuntimeConfig(accessMode string, regs []lipsdk.Registration) (RuntimeConfig, error) {
	multiUser := strings.TrimSpace(accessMode) == "multi_user"
	out := RuntimeConfig{}
	matches, err := EnabledRegistrations(regs)
	if err != nil {
		return RuntimeConfig{}, err
	}
	if len(matches) == 0 {
		return out, nil
	}
	decoded, err := DecodeConfig(matches[0].Config.Node)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("runtimebundle: secrets-guard config: %w", err)
	}
	if err := ValidateAccessMode(decoded, multiUser, matches[0].Config.Node); err != nil {
		return RuntimeConfig{}, fmt.Errorf("runtimebundle: secrets-guard config: %w", err)
	}
	out.Enabled = true
	out.Action = decoded.Action
	out.AuditConfigVersion = runtimeConfigVersion(matches[0].Config.Node)
	out.AuditFailurePolicy = decoded.AuditFailurePolicy
	out.IncludePopularEnv, out.IncludeEnv, out.ExcludeEnv, out.MinSecretBytes, _, out.MaskByte, out.PreserveKnownPrefixes =
		CompositionOptions(decoded)
	return out, nil
}

// EnabledRegistrations returns the enabled secrets-guard feature registrations in config order.
// It rejects more than one enabled secrets-guard registration so startup cannot silently pick an
// arbitrary instance.
func EnabledRegistrations(regs []lipsdk.Registration) ([]lipsdk.Registration, error) {
	out := make([]lipsdk.Registration, 0, len(regs))
	for _, r := range regs {
		if r.Kind != lipsdk.PluginKindFeature || !r.Enabled || !isSecretsGuardRegistration(r) {
			continue
		}
		out = append(out, r)
		if len(out) > 1 {
			return nil, fmt.Errorf("runtimebundle: multiple enabled secrets-guard registrations")
		}
	}
	return out, nil
}

func isSecretsGuardRegistration(r lipsdk.Registration) bool {
	return strings.EqualFold(strings.TrimSpace(r.RegistryFactoryKey()), ID)
}

func runtimeConfigVersion(n yaml.Node) string {
	raw, err := yaml.Marshal(&n)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sg-" + hex.EncodeToString(sum[:8])
}
