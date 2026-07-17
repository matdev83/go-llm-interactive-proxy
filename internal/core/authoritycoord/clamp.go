package authoritycoord

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"

// mergeClampsNonWidening merges provider clamps so max_output_tokens and max_spend
// only tighten (never increase) existing limits.
func mergeClampsNonWidening(dst []authority.Clamp, add []authority.Clamp) []authority.Clamp {
	out := append([]authority.Clamp(nil), dst...)
	for _, c := range add {
		idx := clampIndex(out, c.Kind)
		if idx < 0 {
			out = append(out, c)
			continue
		}
		switch c.Kind {
		case authority.ClampMaxOutputTokens:
			if out[idx].Value == 0 || (c.Value > 0 && c.Value < out[idx].Value) {
				out[idx] = c
			}
		case authority.ClampMaxSpend:
			if !out[idx].Money.Present || (c.Money.Present && c.Money.NanoUnits < out[idx].Money.NanoUnits) {
				out[idx] = c
			}
		default:
			// Unknown clamp kinds are ignored here; preview validation rejects them.
		}
	}
	return out
}

func clampIndex(clamps []authority.Clamp, kind authority.ClampKind) int {
	for i, c := range clamps {
		if c.Kind == kind {
			return i
		}
	}
	return -1
}

// AggregateReadiness picks the most severe readiness among inputs.
func AggregateReadiness(values ...authority.Readiness) authority.Readiness {
	worst := authority.ReadinessReady
	rank := func(r authority.Readiness) int {
		switch r {
		case "", authority.ReadinessReady:
			return 0
		case authority.ReadinessDegraded:
			return 1
		case authority.ReadinessDisabled:
			return 2
		case authority.ReadinessUnavailable:
			return 3
		default:
			return 1
		}
	}
	for _, r := range values {
		if rank(r) > rank(worst) {
			worst = r
		}
	}
	if worst == "" {
		return authority.ReadinessReady
	}
	return worst
}
