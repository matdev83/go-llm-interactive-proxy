package cursorsdk

import (
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type catalogEntry struct {
	NativeID    string
	CanonicalID string
	DisplayName string
	Parameters  []protocol.ModelParameter
	Variants    []protocol.ModelVariant
}

type Catalog struct {
	mu       sync.RWMutex
	byNative map[string]catalogEntry
}

func NewCatalog() *Catalog {
	return &Catalog{byNative: make(map[string]catalogEntry)}
}

func (c *Catalog) Replace(entries []catalogEntry) {
	if c == nil {
		return
	}
	next := make(map[string]catalogEntry, len(entries))
	for _, e := range entries {
		native := strings.TrimSpace(e.NativeID)
		if native == "" {
			continue
		}
		next[native] = e
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byNative = next
}

func (c *Catalog) Entry(native string) (catalogEntry, bool) {
	if c == nil {
		return catalogEntry{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.byNative[strings.TrimSpace(native)]
	return e, ok
}

func (c *Catalog) CapsFor(native string) lipapi.BackendCaps {
	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming)
	e, ok := c.Entry(native)
	if !ok {
		return caps
	}
	if advertisesControllableReasoning(e) {
		caps[lipapi.CapabilityReasoning] = struct{}{}
	}
	return caps
}

func advertisesControllableReasoning(e catalogEntry) bool {
	hasBooleanThinkingOnly := false
	hasReasoningValues := false
	hasEffortWithThinkingVariant := false
	for _, p := range e.Parameters {
		switch strings.TrimSpace(p.ID) {
		case "reasoning":
			if len(p.Values) > 0 {
				hasReasoningValues = true
			}
		case "effort":
			if len(p.Values) > 0 && hasEffortThinkingVariant(e, p.Values) {
				hasEffortWithThinkingVariant = true
			}
		case "thinking":
			if strings.EqualFold(strings.TrimSpace(p.Type), "boolean") {
				hasBooleanThinkingOnly = true
			}
		}
	}
	if hasReasoningValues || hasEffortWithThinkingVariant {
		return true
	}
	_ = hasBooleanThinkingOnly
	return false
}

func hasEffortThinkingVariant(e catalogEntry, effortValues []string) bool {
	allowed := make(map[string]struct{}, len(effortValues))
	for _, v := range effortValues {
		allowed[strings.TrimSpace(v)] = struct{}{}
	}
	for _, variant := range e.Variants {
		effort, ok := variant.Params["effort"]
		if !ok {
			continue
		}
		effortStr, ok := effort.(string)
		if !ok {
			continue
		}
		if _, ok := allowed[strings.TrimSpace(effortStr)]; !ok {
			continue
		}
		thinking, ok := variant.Params["thinking"]
		if !ok {
			continue
		}
		if thinkingBool, ok := thinking.(bool); ok && thinkingBool {
			return true
		}
	}
	return false
}
