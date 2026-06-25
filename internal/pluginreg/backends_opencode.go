package pluginreg

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/opencodecommon"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/opencodego"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/opencodezen"
	"gopkg.in/yaml.v3"
)

func backendOpenCodeGo(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys, vendorResolver opencodecommon.VendorResolver) (execbackend.Backend, error) {
	y, base, ek, models, err := decodeOpenCodeBackendYAML(n, opencodecommon.BackendGo, keys.OpenCodeGo)
	if err != nil {
		return execbackend.Backend{}, fmt.Errorf("opencode-go backend config: %w", err)
	}
	cfg := opencodego.Config{
		BaseURL:        base,
		APIKeys:        ek,
		Credentials:    hostedCredentials(y.Credentials),
		HTTPClient:     resolveUpstreamHTTP(upstream),
		Models:         models,
		VendorResolver: vendorResolver,
	}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return applyConfiguredModelInventory(opencodego.New(cfg), y.Models)
}

func backendOpenCodeZen(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys, vendorResolver opencodecommon.VendorResolver) (execbackend.Backend, error) {
	y, base, ek, models, err := decodeOpenCodeBackendYAML(n, opencodecommon.BackendZen, keys.OpenCodeZen)
	if err != nil {
		return execbackend.Backend{}, fmt.Errorf("opencode-zen backend config: %w", err)
	}
	cfg := opencodezen.Config{
		BaseURL:        base,
		APIKeys:        ek,
		Credentials:    hostedCredentials(y.Credentials),
		HTTPClient:     resolveUpstreamHTTP(upstream),
		Models:         models,
		VendorResolver: vendorResolver,
	}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return applyConfiguredModelInventory(opencodezen.New(cfg), y.Models)
}

func decodeOpenCodeBackendYAML(
	n yaml.Node,
	kind opencodecommon.BackendKind,
	envKeys []string,
) (openAIStyleYAML, string, []string, []opencodecommon.ModelEntry, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return openAIStyleYAML{}, "", nil, nil, err
	}
	base := strings.TrimSpace(y.BaseURL)
	if base == "" {
		switch kind {
		case opencodecommon.BackendGo:
			base = "https://opencode.ai/zen/go/v1"
		case opencodecommon.BackendZen:
			base = "https://opencode.ai/zen/v1"
		default:
			return openAIStyleYAML{}, "", nil, nil, fmt.Errorf("unsupported opencode backend kind %q", kind)
		}
	}
	ek := inventoryAPIKeys(y.APIKey, y.APIKeys, y.Credentials, envKeys)
	models, err := opencodeModelEntriesFromYAML(kind, y.Models)
	if err != nil {
		return openAIStyleYAML{}, "", nil, nil, err
	}
	return y, base, ek, models, nil
}

func opencodeModelEntriesFromYAML(kind opencodecommon.BackendKind, y modelInventoryYAML) ([]opencodecommon.ModelEntry, error) {
	rows, _, ok, err := modelInventoryRows(y, true)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	prefix := opencodecommon.WirePrefix(kind) + "/"
	entries := make([]opencodecommon.ModelEntry, 0, len(rows))
	for i, row := range rows {
		raw := strings.TrimSpace(row.NativeID)
		switch {
		case strings.HasPrefix(raw, prefix):
			raw = strings.TrimPrefix(raw, prefix)
		case raw == "":
			canonical := strings.TrimSpace(row.CanonicalID)
			if canonical == "" {
				return nil, fmt.Errorf("backend models: item[%d] requires native_id or canonical_id", i)
			}
			if idx := strings.LastIndex(canonical, "/"); idx >= 0 {
				raw = canonical[idx+1:]
			} else {
				raw = canonical
			}
		}
		if raw == "" {
			return nil, fmt.Errorf("backend models: item[%d] requires a model id", i)
		}
		entries = append(entries, opencodecommon.ModelEntry{
			RawID:       raw,
			DisplayName: strings.TrimSpace(row.DisplayName),
		})
	}
	return entries, nil
}
