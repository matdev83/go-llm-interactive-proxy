// Package aggregate applies metering fact kinds onto a stream aggregate without
// mutating journal history (requirements 3.2, 3.3, 3.5, 13.6).
package aggregate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Snapshot is the idempotent result of replaying an ordered fact stream.
type Snapshot struct {
	StreamID      string
	Quantities    map[string]int64 // component -> value (present only)
	MoneyNano     int64
	MoneyCurrency string
	MoneyPresent  bool
	Unavailable   []string // fact IDs marked unavailable / unresolved
	Superseded    map[string]struct{}
	LastSequence  int64
}

// Apply replays facts in Sequence order (stable by FactID on ties). Replaying
// the same ordered set yields the same Snapshot (restart hydration).
func Apply(facts []metering.Fact) (Snapshot, error) {
	ordered := append([]metering.Fact(nil), facts...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Sequence != ordered[j].Sequence {
			return ordered[i].Sequence < ordered[j].Sequence
		}
		return ordered[i].FactID < ordered[j].FactID
	})
	snap := Snapshot{
		Quantities: make(map[string]int64),
		Superseded: make(map[string]struct{}),
	}
	seen := make(map[string]struct{}) // FactID for idempotent skip of exact replays in input
	for _, f := range ordered {
		if err := f.Validate(); err != nil {
			return Snapshot{}, fmt.Errorf("metering/aggregate: %w", err)
		}
		if snap.StreamID == "" {
			snap.StreamID = f.StreamID
		} else if f.StreamID != snap.StreamID {
			return Snapshot{}, fmt.Errorf("metering/aggregate: mixed stream_id %q and %q", snap.StreamID, f.StreamID)
		}
		idKey := f.FactID
		if _, ok := seen[idKey]; ok {
			continue // duplicate identity in batch is a no-op
		}
		seen[idKey] = struct{}{}
		if f.Sequence > snap.LastSequence {
			snap.LastSequence = f.Sequence
		}
		switch f.Kind {
		case metering.FactKindUnavailable:
			snap.Unavailable = appendUnique(snap.Unavailable, f.FactID)
			continue
		case metering.FactKindReservationEstimate:
			// Estimates do not settle into the metering total; tracked for reconcile only.
			continue
		case metering.FactKindDelta:
			applyDelta(snap.Quantities, f.Quantities, 1)
			applyMoneyDelta(&snap, f.Money, 1)
		case metering.FactKindCumulative:
			replacePresent(snap.Quantities, f.Quantities)
			applyMoneyReplace(&snap, f.Money)
		case metering.FactKindCorrection:
			for _, id := range f.Supersedes {
				snap.Superseded[strings.TrimSpace(id)] = struct{}{}
			}
			applyDelta(snap.Quantities, f.Quantities, 1)
			applyMoneyDelta(&snap, f.Money, 1)
		case metering.FactKindAuthoritativeReplacement:
			for _, id := range f.Supersedes {
				snap.Superseded[strings.TrimSpace(id)] = struct{}{}
			}
			snap.Quantities = make(map[string]int64)
			replacePresent(snap.Quantities, f.Quantities)
			applyMoneyReplace(&snap, f.Money)
		default:
			return Snapshot{}, fmt.Errorf("metering/aggregate: unsupported kind %q", f.Kind)
		}
	}
	return snap, nil
}

func applyDelta(dst map[string]int64, qs []metering.Quantity, sign int64) {
	for _, q := range qs {
		if !q.Present {
			continue
		}
		dst[q.Component] += sign * q.Value
	}
}

func replacePresent(dst map[string]int64, qs []metering.Quantity) {
	for _, q := range qs {
		if !q.Present {
			continue
		}
		dst[q.Component] = q.Value
	}
}

func applyMoneyDelta(snap *Snapshot, m *metering.MoneyObservation, sign int64) {
	if m == nil || !m.Present {
		return
	}
	snap.MoneyPresent = true
	snap.MoneyNano += sign * m.NanoUnits
	if cur := strings.TrimSpace(m.Currency); cur != "" {
		snap.MoneyCurrency = cur
	}
}

func applyMoneyReplace(snap *Snapshot, m *metering.MoneyObservation) {
	if m == nil || !m.Present {
		snap.MoneyPresent = false
		snap.MoneyNano = 0
		snap.MoneyCurrency = ""
		return
	}
	snap.MoneyPresent = true
	snap.MoneyNano = m.NanoUnits
	snap.MoneyCurrency = strings.TrimSpace(m.Currency)
}

func appendUnique(in []string, v string) []string {
	for _, x := range in {
		if x == v {
			return in
		}
	}
	return append(in, v)
}
