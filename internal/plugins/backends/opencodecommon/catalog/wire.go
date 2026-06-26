package catalog

import (
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type BackendKind string

const (
	BackendGo  BackendKind = "opencode-go"
	BackendZen BackendKind = "opencode-zen"
)

func WirePrefix(kind BackendKind) string {
	switch kind {
	case BackendGo:
		return "opencode-go"
	case BackendZen:
		return "opencode"
	default:
		return strings.TrimSpace(string(kind))
	}
}

func NativeID(kind BackendKind, rawID string) string {
	rawID = strings.TrimSpace(rawID)
	if rawID == "" {
		return "unknown"
	}
	return rawID
}

func InventoryModels(kind BackendKind, entries []ModelEntry, vendors modelcatalog.VendorResolver) []modelinventory.Model {
	canonicalizer := NewCanonicalizer(vendors)
	models := make([]modelinventory.Model, 0, len(entries))
	for _, entry := range entries {
		rawID := strings.TrimSpace(entry.RawID)
		if rawID == "" {
			continue
		}
		display := strings.TrimSpace(entry.DisplayName)
		if display == "" {
			display = rawID
		}
		models = append(models, modelinventory.Model{
			CanonicalID: canonicalizer.CanonicalID(rawID),
			NativeID:    NativeID(kind, rawID),
			DisplayName: display,
		})
	}
	return slices.Clone(models)
}
