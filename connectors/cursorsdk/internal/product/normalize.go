package product

import (
	"fmt"
	"maps"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

const vendorPrefix = "cursor"

func canonicalIDForNative(native string) string {
	path := strings.TrimSpace(native)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, vendorPrefix+"/") {
		return path
	}
	if after, ok := strings.CutPrefix(path, vendorPrefix+"-"); ok {
		path = after
	}
	return vendorPrefix + "/" + path
}

func normalizeModelRows(rows []protocol.ModelRow) ([]modelinventory.Model, []catalogEntry, error) {
	seen := make(map[string]struct{}, len(rows))
	models := make([]modelinventory.Model, 0, len(rows))
	entries := make([]catalogEntry, 0, len(rows))
	for i, row := range rows {
		native := strings.TrimSpace(row.ID)
		if native == "" {
			return nil, nil, fmt.Errorf("cursorsdk: models[%d]: empty id", i)
		}
		if _, dup := seen[native]; dup {
			return nil, nil, fmt.Errorf("cursorsdk: duplicate model id %q", native)
		}
		seen[native] = struct{}{}
		display := strings.TrimSpace(row.DisplayName)
		if display == "" {
			display = native
		}
		variants, err := normalizeVariants(row.Variants, i)
		if err != nil {
			return nil, nil, err
		}
		canonical := canonicalIDForNative(native)
		models = append(models, modelinventory.Model{
			CanonicalID: canonical,
			NativeID:    native,
			DisplayName: display,
		})
		entries = append(entries, catalogEntry{
			NativeID:    native,
			CanonicalID: canonical,
			DisplayName: display,
			Parameters:  cloneParameters(row.Parameters),
			Variants:    variants,
		})
	}
	return models, entries, nil
}

func cloneParameters(in []protocol.ModelParameter) []protocol.ModelParameter {
	if len(in) == 0 {
		return nil
	}
	out := make([]protocol.ModelParameter, len(in))
	for i, p := range in {
		out[i] = protocol.ModelParameter{
			ID:     strings.TrimSpace(p.ID),
			Type:   strings.TrimSpace(p.Type),
			Values: append([]string(nil), p.Values...),
		}
	}
	return out
}

func normalizeVariants(in []protocol.ModelVariant, modelIndex int) ([]protocol.ModelVariant, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]protocol.ModelVariant, 0, len(in))
	for vi, v := range in {
		id := strings.TrimSpace(v.ID)
		params := make(map[string]any, len(v.Params))
		maps.Copy(params, v.Params)
		if id == "" && len(params) == 0 {
			return nil, fmt.Errorf("cursorsdk: models[%d].variants[%d]: anonymous variant requires nonempty params", modelIndex, vi)
		}
		out = append(out, protocol.ModelVariant{
			ID:          id,
			DisplayName: strings.TrimSpace(v.DisplayName),
			Params:      params,
		})
	}
	return out, nil
}
