package toolcallrepair

import (
	"bytes"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type CatalogIndex struct {
	exact      map[string]lipapi.ToolDef
	uniqueNorm map[string]lipapi.ToolDef
}

func BuildCatalogIndex(catalog []lipapi.ToolDef) *CatalogIndex {
	idx := &CatalogIndex{
		exact:      make(map[string]lipapi.ToolDef, len(catalog)),
		uniqueNorm: make(map[string]lipapi.ToolDef, len(catalog)),
	}
	normCounts := make(map[string]int, len(catalog))
	normFirst := make(map[string]lipapi.ToolDef, len(catalog))
	for _, tool := range catalog {
		owned := cloneToolDef(tool)
		idx.exact[owned.Name] = owned
		norm := NormalizeASCIIName(owned.Name)
		if norm == "" {
			continue
		}
		normCounts[norm]++
		if _, ok := normFirst[norm]; !ok {
			normFirst[norm] = owned
		}
	}
	for norm, n := range normCounts {
		if n == 1 {
			idx.uniqueNorm[norm] = normFirst[norm]
		}
	}
	return idx
}

func cloneToolDef(tool lipapi.ToolDef) lipapi.ToolDef {
	if tool.Parameters != nil {
		tool.Parameters = bytes.Clone(tool.Parameters)
	}
	return tool
}

func (idx *CatalogIndex) Exact(name string) (lipapi.ToolDef, bool) {
	if idx == nil {
		return lipapi.ToolDef{}, false
	}
	tool, ok := idx.exact[name]
	return tool, ok
}

func (idx *CatalogIndex) UniqueNormalized(name string) (lipapi.ToolDef, bool) {
	if idx == nil {
		return lipapi.ToolDef{}, false
	}
	tool, ok := idx.uniqueNorm[NormalizeASCIIName(name)]
	return tool, ok
}
