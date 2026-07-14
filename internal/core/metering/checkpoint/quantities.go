package checkpoint

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// QuantitiesFromTokenCounts maps token components to metering quantities using
// the default inclusion schema vocabulary. Zero counts remain Present when
// present is true; omitted totals leave PresenceUnknown on the checkpoint.
func QuantitiesFromTokenCounts(input, output, cacheRead, cacheWrite, reasoning, total int64, totalPresent bool) []metering.Quantity {
	out := []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: input, Present: true},
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: output, Present: true},
		{Component: metering.ComponentCacheReadInputToken, Unit: metering.UnitToken, Value: cacheRead, Present: true},
		{Component: metering.ComponentCacheWriteInputToken, Unit: metering.UnitToken, Value: cacheWrite, Present: true},
		{Component: metering.ComponentReasoningOutputToken, Unit: metering.UnitToken, Value: reasoning, Present: true},
	}
	if totalPresent {
		out = append(out, metering.Quantity{
			Component: metering.ComponentTotalToken,
			Unit:      metering.UnitToken,
			Value:     total,
			Present:   true,
		})
	}
	return out
}

// ApplyQuantities sets Public.Quantities and updates Presence.
func (s *Snapshot) ApplyQuantities(qs []metering.Quantity) {
	if s == nil {
		return
	}
	s.Public.Quantities = qs
	if len(qs) == 0 {
		s.Public.Presence = metering.PresenceUnknown
		return
	}
	s.Public.Presence = metering.PresencePresent
}
