package diag

import (
	"encoding/json"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

// InventorySnapshot is a JSON-serializable view of configured plugins for operators.
type InventorySnapshot struct {
	Frontends []PluginRow `json:"frontends"`
	Backends  []PluginRow `json:"backends"`
	Features  []PluginRow `json:"features"`
}

// PluginRow is one config row (instance id + factory kind + enabled; config payloads stay opaque/private).
type PluginRow struct {
	ID          string `json:"id"`
	FactoryKind string `json:"factory_kind"`
	Enabled     bool   `json:"enabled"`
}

// InventoryHandler serves GET JSON describing enabled plugin rows from cfg.
func InventoryHandler(cfg *config.Config) http.Handler {
	if cfg == nil {
		panic("diag: InventoryHandler: nil config")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		snap := InventorySnapshot{
			Frontends: rows(cfg.Plugins.Frontends),
			Backends:  rows(cfg.Plugins.Backends),
			Features:  rows(cfg.Plugins.Features),
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(true)
		_ = enc.Encode(snap)
	})
}

func rows(in []config.PluginConfig) []PluginRow {
	out := make([]PluginRow, 0, len(in))
	for _, p := range in {
		out = append(out, PluginRow{
			ID:          p.InstanceID(),
			FactoryKind: p.FactoryID(),
			Enabled:     p.Enabled,
		})
	}
	return out
}
