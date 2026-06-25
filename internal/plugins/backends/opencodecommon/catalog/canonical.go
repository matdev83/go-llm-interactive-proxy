package catalog

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
)

var canonicalAliases = map[string]string{
	"kimi-k2.7": "kimi-k2.7-code",
}

type VendorResolver interface {
	CanonicalID(model string) string
}

type Canonicalizer struct {
	vendors VendorResolver
}

func NewCanonicalizer(vendors VendorResolver) *Canonicalizer {
	if vendors == nil {
		vendors = NewModelCatalogVendorResolver(NewOpenCodeVendorResolver(modelcatalog.StaticActiveSnapshotProvider{}, true))
	}
	return &Canonicalizer{vendors: vendors}
}

type ModelCatalogVendorResolver interface {
	Resolve(model string) modelcatalog.VendorResolveResult
}

type modelCatalogVendorResolver struct {
	resolver ModelCatalogVendorResolver
}

func NewModelCatalogVendorResolver(resolver ModelCatalogVendorResolver) VendorResolver {
	return modelCatalogVendorResolver{resolver: resolver}
}

func (c *Canonicalizer) CanonicalID(rawID string) string {
	rawID = strings.TrimSpace(rawID)
	if rawID == "" {
		return "unknown/unknown"
	}
	model := canonicalModelName(rawID)
	if got := c.vendors.CanonicalID(model); got != "" {
		return got
	}
	return "unknown/" + rawID
}

func (r modelCatalogVendorResolver) CanonicalID(model string) string {
	if r.resolver == nil {
		return ""
	}
	return r.resolver.Resolve(model).CanonicalID
}

func canonicalModelName(rawID string) string {
	rawID = strings.TrimSpace(rawID)
	if aliased, ok := canonicalAliases[strings.ToLower(rawID)]; ok {
		return aliased
	}
	return rawID
}
