package authoritycoord

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
)

// mergeClampsNonWidening merges provider clamps so max_output_tokens and max_spend
// only tighten (never increase) existing limits. Mixed-currency spend clamps are rejected.
func mergeClampsNonWidening(dst []authority.Clamp, add []authority.Clamp) ([]authority.Clamp, error) {
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
			if err := assertSameSpendCurrency(out[idx], c); err != nil {
				return nil, err
			}
			if !out[idx].Money.Present || (c.Money.Present && c.Money.NanoUnits < out[idx].Money.NanoUnits) {
				out[idx] = c
			}
		default:
			// Unknown clamp kinds are ignored here; preview validation rejects them.
		}
	}
	return out, nil
}

func assertSameSpendCurrency(existing, incoming authority.Clamp) error {
	if existing.Kind != authority.ClampMaxSpend || incoming.Kind != authority.ClampMaxSpend {
		return nil
	}
	if !existing.Money.Present || !incoming.Money.Present {
		return nil
	}
	a := strings.TrimSpace(existing.Money.Currency)
	b := strings.TrimSpace(incoming.Money.Currency)
	if a == "" || b == "" {
		return fmt.Errorf("max_spend clamp requires currency")
	}
	if !strings.EqualFold(a, b) {
		return fmt.Errorf("mixed-currency max_spend clamps %q and %q", a, b)
	}
	return nil
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
