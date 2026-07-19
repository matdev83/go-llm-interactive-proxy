package cursorsdk

import (
	"slices"
	"strings"
)

type reasoningMode string

const (
	reasoningModeNone      reasoningMode = ""
	reasoningModeReasoning reasoningMode = "reasoning"
	reasoningModeEffort    reasoningMode = "effort"
)

type reasoningProfile struct {
	Mode   reasoningMode
	Values []string
}

func (c *Catalog) reasoningProfile(native string) reasoningProfile {
	e, ok := c.Entry(native)
	if !ok {
		return reasoningProfile{}
	}
	return profileFromEntry(e)
}

func profileFromEntry(e catalogEntry) reasoningProfile {
	for _, p := range e.Parameters {
		if strings.TrimSpace(p.ID) != "reasoning" || len(p.Values) == 0 {
			continue
		}
		return reasoningProfile{
			Mode:   reasoningModeReasoning,
			Values: append([]string(nil), p.Values...),
		}
	}
	var effortVals []string
	for _, p := range e.Parameters {
		if strings.TrimSpace(p.ID) == "effort" && len(p.Values) > 0 {
			effortVals = p.Values
			break
		}
	}
	if len(effortVals) == 0 || !hasEffortThinkingVariant(e, effortVals) {
		return reasoningProfile{}
	}
	accepted := make([]string, 0, len(effortVals))
	allowed := make(map[string]struct{}, len(effortVals))
	for _, v := range effortVals {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		allowed[v] = struct{}{}
	}
	for _, variant := range e.Variants {
		effort, ok := variant.Params["effort"].(string)
		if !ok {
			continue
		}
		effort = strings.TrimSpace(effort)
		if _, ok := allowed[effort]; !ok {
			continue
		}
		thinking, ok := variant.Params["thinking"].(bool)
		if !ok || !thinking {
			continue
		}
		accepted = appendUnique(accepted, effort)
	}
	if len(accepted) == 0 {
		return reasoningProfile{}
	}
	return reasoningProfile{Mode: reasoningModeEffort, Values: accepted}
}

func (p reasoningProfile) acceptsExact(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || p.Mode == reasoningModeNone {
		return false
	}
	return slices.Contains(p.Values, value)
}

func appendUnique(dst []string, v string) []string {
	if slices.Contains(dst, v) {
		return dst
	}
	return append(dst, v)
}
