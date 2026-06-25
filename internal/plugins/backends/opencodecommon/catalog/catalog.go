package catalog

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
)

var ErrUnknownModel = errors.New("opencodecommon: unknown model")

type ResolvedModel struct {
	RawID     string
	WireModel string
	Flavor    Flavor
	Entry     ModelEntry
}

type ModelCatalog struct {
	kind          BackendKind
	prefix        string
	canonicalizer *Canonicalizer
	byCanonical   map[string]ModelEntry
	byNative      map[string]ModelEntry
	byRaw         map[string]ModelEntry
}

func NewModelCatalog(kind BackendKind, entries []ModelEntry, vendors VendorResolver) *ModelCatalog {
	c := &ModelCatalog{
		kind:          kind,
		prefix:        WirePrefix(kind),
		canonicalizer: NewCanonicalizer(vendors),
		byCanonical:   make(map[string]ModelEntry, len(entries)),
		byNative:      make(map[string]ModelEntry, len(entries)),
		byRaw:         make(map[string]ModelEntry, len(entries)),
	}
	for _, entry := range entries {
		rawID := strings.TrimSpace(entry.RawID)
		if rawID == "" {
			continue
		}
		c.byCanonical[c.canonicalizer.CanonicalID(rawID)] = entry
		c.byNative[NativeID(kind, rawID)] = entry
		c.byRaw[rawID] = entry
		c.byRaw[strings.ToLower(rawID)] = entry
	}
	return c
}

func (c *ModelCatalog) Resolve(model string) (ResolvedModel, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return ResolvedModel{}, fmt.Errorf("%w: empty model", ErrUnknownModel)
	}

	entry, ok := c.lookup(model)
	if !ok {
		return ResolvedModel{}, fmt.Errorf("%w: %q", ErrUnknownModel, model)
	}
	rawID := strings.TrimSpace(entry.RawID)
	return ResolvedModel{
		RawID:     rawID,
		WireModel: rawID,
		Flavor:    InferFlavor(entry),
		Entry:     entry,
	}, nil
}

func (c *ModelCatalog) lookup(model string) (ModelEntry, bool) {
	if entry, ok := c.byNative[model]; ok {
		return entry, true
	}
	if entry, ok := c.byCanonical[model]; ok {
		return entry, true
	}
	if canonical := c.canonicalizer.CanonicalID(model); canonical != model {
		if entry, ok := c.byCanonical[canonical]; ok {
			return entry, true
		}
	}
	if entry, ok := c.byRaw[model]; ok {
		return entry, true
	}
	lower := strings.ToLower(model)
	if entry, ok := c.byRaw[lower]; ok {
		return entry, true
	}
	prefix := c.prefix + "/"
	if strings.HasPrefix(model, prefix) {
		raw := strings.TrimPrefix(model, prefix)
		if entry, ok := c.byRaw[raw]; ok {
			return entry, true
		}
		if entry, ok := c.byRaw[strings.ToLower(raw)]; ok {
			return entry, true
		}
	}
	return ModelEntry{}, false
}

func (c *ModelCatalog) entries() []ModelEntry {
	seen := make(map[string]struct{}, len(c.byRaw))
	out := make([]ModelEntry, 0, len(c.byRaw))
	for _, entry := range c.byRaw {
		rawID := entry.RawID
		if _, ok := seen[rawID]; ok {
			continue
		}
		seen[rawID] = struct{}{}
		out = append(out, entry)
	}
	slices.SortFunc(out, func(a, b ModelEntry) int {
		return cmp.Compare(a.RawID, b.RawID)
	})
	return out
}
