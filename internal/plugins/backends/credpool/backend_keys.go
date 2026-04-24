package credpool

import (
	"fmt"
	"strings"
)

// BackendKeySecrets returns ordered non-empty trimmed secrets for a hosted backend
// instance: api_key first (when non-empty after trim), then api_keys in order.
// Duplicate secrets are dropped while preserving first-seen order (api_key then api_keys,
// aligned with pluginreg.EffectiveAPIKeys for the YAML credential fields).
func BackendKeySecrets(apiKey string, apiKeys []string) ([]string, error) {
	n := 1 + len(apiKeys)
	seen := make(map[string]struct{}, n)
	out := make([]string, 0, n)
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	add(apiKey)
	for _, k := range apiKeys {
		add(k)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("credpool: no API credentials")
	}
	return out, nil
}

// NewPoolFromBackendKeys builds a pool from install-style API key fields.
func NewPoolFromBackendKeys(apiKey string, apiKeys []string) (*Pool, error) {
	secrets, err := BackendKeySecrets(apiKey, apiKeys)
	if err != nil {
		return nil, err
	}
	creds := make([]Credential, len(secrets))
	for i, s := range secrets {
		creds[i] = Credential{Secret: s}
	}
	return New(creds)
}
