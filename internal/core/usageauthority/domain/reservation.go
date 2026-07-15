package domain

import (
	"fmt"
	"strings"
)

type ReservationKey struct {
	LogicalRequestID string
	ALegID           string
	BLegID           string
	AttemptID        string
	RuleID           string
	Sequence         int
	// Namespace isolates dual-plane reservations. Empty keeps the pre-Phase-7
	// key string format for legacy compatibility.
	Namespace string
}

func (k ReservationKey) String() string {
	base := fmt.Sprintf("%s|%s|%s|%s|%s|%d", k.LogicalRequestID, k.ALegID, k.BLegID, k.AttemptID, k.RuleID, k.Sequence)
	if ns := strings.TrimSpace(k.Namespace); ns != "" {
		return ns + "|" + base
	}
	return base
}

type SettlementKey struct {
	ReservationKey ReservationKey
	Sequence       int
}

func (k SettlementKey) String() string {
	return fmt.Sprintf("%s|settle|%d", k.ReservationKey.String(), k.Sequence)
}

type ReleaseKey struct {
	ReservationKey ReservationKey
	Sequence       int
}

func (k ReleaseKey) String() string {
	return fmt.Sprintf("%s|release|%d", k.ReservationKey.String(), k.Sequence)
}

type SettlementResult struct {
	Applied       bool
	ConsumedDelta Amount
	ReleasedDelta Amount
	OverageDelta  Amount
}

type ReleaseResult struct {
	Applied       bool
	ReleasedDelta Amount
}

type WindowBalance struct {
	Consumed Amount
	Reserved Amount
	Released Amount
	Overage  Amount

	settlements map[string]struct{}
	releases    map[string]struct{}
}

func (b *WindowBalance) Settle(key SettlementKey, reserved, actual Amount) SettlementResult {
	if b.settlements == nil {
		b.settlements = map[string]struct{}{}
	}
	id := key.String()
	if _, ok := b.settlements[id]; ok {
		return SettlementResult{}
	}
	b.settlements[id] = struct{}{}

	result := SettlementResult{Applied: true, ConsumedDelta: actual}
	b.Consumed.Value += actual.Value
	b.Consumed.Unit = actual.Unit
	b.Consumed.Currency = actual.Currency

	if actual.Value < reserved.Value {
		released := reserved.Value - actual.Value
		result.ReleasedDelta = Amount{Unit: reserved.Unit, Value: released, Currency: reserved.Currency}
		b.Released.Value += released
		b.Released.Unit = reserved.Unit
		b.Released.Currency = reserved.Currency
	}
	if actual.Value > reserved.Value {
		overage := actual.Value - reserved.Value
		result.OverageDelta = Amount{Unit: actual.Unit, Value: overage, Currency: actual.Currency}
		b.Overage.Value += overage
		b.Overage.Unit = actual.Unit
		b.Overage.Currency = actual.Currency
	}
	return result
}

func (b *WindowBalance) Release(key ReleaseKey, amount Amount) ReleaseResult {
	if b.releases == nil {
		b.releases = map[string]struct{}{}
	}
	id := key.String()
	if _, ok := b.releases[id]; ok {
		return ReleaseResult{}
	}
	b.releases[id] = struct{}{}

	b.Released.Value += amount.Value
	b.Released.Unit = amount.Unit
	b.Released.Currency = amount.Currency
	return ReleaseResult{
		Applied:       true,
		ReleasedDelta: amount,
	}
}
