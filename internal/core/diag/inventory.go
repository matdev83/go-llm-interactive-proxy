package diag

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

// InventorySnapshot is a JSON-serializable view of configured plugins for operators.
type InventorySnapshot struct {
	Frontends  []PluginRow         `json:"frontends"`
	Backends   []PluginRow         `json:"backends"`
	Features   []PluginRow         `json:"features"`
	Extensions InventoryExtensions `json:"extensions"`
}

// PluginRow is one config row (instance id + factory kind + enabled; config payloads stay opaque/private).
type PluginRow struct {
	ID          string `json:"id"`
	FactoryKind string `json:"factory_kind"`
	Enabled     bool   `json:"enabled"`
}

// InventoryHandler serves GET JSON describing enabled plugin rows from cfg.
// extras may be nil; when extras.Reg is set, extension occupancy is resolved from live factories.
func InventoryHandler(cfg *config.Config, extras *InventoryExtras) (http.Handler, error) {
	if cfg == nil {
		return nil, errors.New("diag: InventoryHandler: nil config")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		snap := InventorySnapshot{
			Frontends:  rows(cfg.Plugins.Frontends),
			Backends:   rows(cfg.Plugins.Backends),
			Features:   rows(cfg.Plugins.Features),
			Extensions: buildInventoryExtensions(r.Context(), cfg, extras),
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(true)
		if err := enc.Encode(snap); err != nil {
			slog.ErrorContext(r.Context(), "diag: inventory encode", "error", err)
		}
	}), nil
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
