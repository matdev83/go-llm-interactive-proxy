package pluginreg

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

type openAIStyleYAML struct {
	BaseURL     string                 `yaml:"base_url"`
	APIKey      string                 `yaml:"api_key"`
	APIKeys     []string               `yaml:"api_keys"`
	Credentials []hostedCredentialYAML `yaml:"credentials"`
	Models      modelInventoryYAML     `yaml:"models"`
}

type hostedCredentialYAML struct {
	ID                string `yaml:"id"`
	APIKey            string `yaml:"api_key"`
	RemoteOrgID       string `yaml:"remote_org_id"`
	RemoteProjectID   string `yaml:"remote_project_id"`
	RemoteWorkspaceID string `yaml:"remote_workspace_id"`
	RemoteAccountID   string `yaml:"remote_account_id"`
	RemoteRegion      string `yaml:"remote_region"`
}

type modelInventoryYAML struct {
	Source string                   `yaml:"source"`
	Path   string                   `yaml:"path"`
	Items  []modelInventoryItemYAML `yaml:"items"`
}

type modelInventoryFileYAML struct {
	Items  []modelInventoryItemYAML `yaml:"items"`
	Models []modelInventoryItemYAML `yaml:"models"`
}

type modelInventoryItemYAML struct {
	CanonicalID string `yaml:"canonical_id"`
	NativeID    string `yaml:"native_id"`
	DisplayName string `yaml:"display_name"`
}

func resolveUpstreamHTTP(upstream *http.Client) *http.Client {
	if upstream != nil {
		return upstream
	}
	return httpclient.Standard()
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

func applyConfiguredModelInventory(be execbackend.Backend, y modelInventoryYAML) (execbackend.Backend, error) {
	provider, ok, err := configuredModelInventory(y)
	if err != nil {
		return execbackend.Backend{}, err
	}
	if ok {
		be.ModelInventory = provider
	}
	return be, nil
}

func configuredModelInventory(y modelInventoryYAML) (modelinventory.Provider, bool, error) {
	rows, source, ok, err := modelInventoryRows(y, false)
	if err != nil || !ok {
		return nil, false, err
	}
	return staticModelInventory(source, rows)
}

func modelInventoryRows(y modelInventoryYAML, emptyOK bool) ([]modelInventoryItemYAML, modelinventory.Source, bool, error) {
	source := strings.ToLower(strings.TrimSpace(y.Source))
	path := strings.TrimSpace(y.Path)
	if path != "" && len(y.Items) > 0 {
		return nil, "", false, fmt.Errorf("backend models: specify either path or items, not both")
	}
	if source == "" {
		if len(y.Items) == 0 && path == "" {
			return nil, "", false, nil
		}
		if path != "" && len(y.Items) == 0 {
			source = "file"
		} else {
			source = "inline"
		}
	}
	var rows []modelInventoryItemYAML
	var inventorySource modelinventory.Source
	switch source {
	case "inline", "static_inline":
		rows = y.Items
		inventorySource = modelinventory.SourceStaticInline
	case "file", "static_file":
		path := strings.TrimSpace(y.Path)
		if path == "" {
			return nil, "", false, fmt.Errorf("backend models: path is required for source %q", source)
		}
		items, err := loadModelInventoryFile(path)
		if err != nil {
			return nil, "", false, err
		}
		rows = items
		inventorySource = modelinventory.SourceStaticFile
	default:
		return nil, "", false, fmt.Errorf("backend models: unsupported source %q", y.Source)
	}
	if len(rows) == 0 {
		if emptyOK {
			return nil, "", false, nil
		}
		return nil, "", false, fmt.Errorf("backend models: at least one item is required")
	}
	return rows, inventorySource, true, nil
}

func loadModelInventoryFile(path string) ([]modelInventoryItemYAML, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("backend models: read %q: %w", path, err)
	}
	var y modelInventoryFileYAML
	if err := yaml.Unmarshal(b, &y); err != nil {
		return nil, fmt.Errorf("backend models: decode %q: %w", path, err)
	}
	if len(y.Items) > 0 {
		return y.Items, nil
	}
	return y.Models, nil
}

func staticModelInventory(source modelinventory.Source, rows []modelInventoryItemYAML) (modelinventory.Provider, bool, error) {
	if len(rows) == 0 {
		return nil, false, fmt.Errorf("backend models: at least one item is required")
	}
	models := make([]modelinventory.Model, 0, len(rows))
	for i, row := range rows {
		canonical := strings.TrimSpace(row.CanonicalID)
		native := strings.TrimSpace(row.NativeID)
		if canonical == "" || native == "" {
			return nil, false, fmt.Errorf("backend models: item[%d] requires canonical_id and native_id", i)
		}
		models = append(models, modelinventory.Model{
			CanonicalID: canonical,
			NativeID:    native,
			DisplayName: strings.TrimSpace(row.DisplayName),
		})
	}
	return modelinventory.StaticProvider{
		Source: source,
		Models: models,
	}, true, nil
}
