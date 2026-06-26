package pluginreg

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
)

type hostedCredentialYAML struct {
	ID                string `yaml:"id"`
	APIKey            string `yaml:"api_key"`
	RemoteOrgID       string `yaml:"remote_org_id"`
	RemoteProjectID   string `yaml:"remote_project_id"`
	RemoteWorkspaceID string `yaml:"remote_workspace_id"`
	RemoteAccountID   string `yaml:"remote_account_id"`
	RemoteRegion      string `yaml:"remote_region"`
}

func hostedCredentials(rows []hostedCredentialYAML) []credpool.Credential {
	if len(rows) == 0 {
		return nil
	}
	out := make([]credpool.Credential, 0, len(rows))
	for _, row := range rows {
		out = append(out, credpool.Credential{
			ID:                strings.TrimSpace(row.ID),
			Secret:            strings.TrimSpace(row.APIKey),
			RemoteOrgID:       strings.TrimSpace(row.RemoteOrgID),
			RemoteProjectID:   strings.TrimSpace(row.RemoteProjectID),
			RemoteWorkspaceID: strings.TrimSpace(row.RemoteWorkspaceID),
			RemoteAccountID:   strings.TrimSpace(row.RemoteAccountID),
			RemoteRegion:      strings.TrimSpace(row.RemoteRegion),
		})
	}
	return out
}

func inventoryAPIKeys(apiKey string, apiKeys []string, credentials []hostedCredentialYAML, fallback []string) []string {
	out := EffectiveAPIKeys(apiKey, apiKeys, fallback)
	for _, cred := range hostedCredentials(credentials) {
		if secret := strings.TrimSpace(cred.Secret); secret != "" {
			out = append(out, secret)
		}
	}
	return out
}

func firstAPIKey(apiKey string, apiKeys []string, credentials []hostedCredentialYAML, fallback []string) ([]string, string) {
	keys := inventoryAPIKeys(apiKey, apiKeys, credentials, fallback)
	return keys, firstResolvedAPIKey(keys)
}

func firstResolvedAPIKey(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}
