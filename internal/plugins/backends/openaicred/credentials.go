package openaicred

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
)

// CredentialsFromConfig builds [credpool.Credential] entries for one backend instance.
// Ordering matches [credpool.BackendKeySecrets] (api_key first, then api_keys; deduplicated).
func CredentialsFromConfig(apiKey string, apiKeys []string) ([]credpool.Credential, error) {
	secrets, err := credpool.BackendKeySecrets(apiKey, apiKeys)
	if err != nil {
		return nil, err
	}
	out := make([]credpool.Credential, len(secrets))
	for i, s := range secrets {
		out[i] = credpool.Credential{Secret: s}
	}
	return out, nil
}
