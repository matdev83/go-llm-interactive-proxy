package billing

import (
	"fmt"
	"sort"
	"strings"
)

type CallRatingInput struct {
	Call              CallUsageRecord
	Legs              []CallLegUsageRecord
	MaxCustomerCharge Money
	CustomerPricing   PricingSnapshot
	CustomerPolicy    ChargePolicy
	OperatorRates     OperatorRateSet
}
type CallRatingResult struct {
	CallID         BillingCallID
	CustomerCharge Money
	Fingerprint    string
}
type ApplyCallBillingInput struct {
	Call          CallUsageRecord
	Exposure      CallExposure
	Result        CallRatingResult
	OperationKind string
}
type CallSettlement struct {
	CallID   BillingCallID
	Customer Posting
	Replayed bool
}

func RateCall(in CallRatingInput) (CallRatingResult, error) {
	call, err := in.Call.Seal()
	if err != nil {
		return CallRatingResult{}, err
	}
	if err := in.MaxCustomerCharge.Validate(); err != nil {
		return CallRatingResult{}, err
	}
	if in.MaxCustomerCharge.Currency != in.CustomerPricing.Currency {
		return CallRatingResult{}, ErrRatingCurrencyMismatch
	}
	if in.CustomerPricing.Ref != call.CustomerPricingRef || in.CustomerPolicy.Ref != call.ChargePolicyRef || in.CustomerPolicy.PricingRef != call.CustomerPricingRef {
		return CallRatingResult{}, ErrRatingSnapshotMismatch
	}
	if err := in.CustomerPolicy.Validate(); err != nil {
		return CallRatingResult{}, err
	}
	turnKey := call.Key
	legs := make([]LegUsageRecord, 0, len(in.Legs))
	legFingerprints := make([]string, 0, len(in.Legs))
	for seq, source := range in.Legs {
		leg, sealErr := source.Seal()
		if sealErr != nil {
			return CallRatingResult{}, sealErr
		}
		if leg.CallID != call.CallID || !containsExpectedLeg(call.ExpectedBLegIDs, leg.BLegID) {
			return CallRatingResult{}, fmt.Errorf("%w: leg %q is not expected for call", ErrRatingSnapshotMismatch, leg.BLegID)
		}
		legs = append(legs, LegUsageRecord{
			ALegID: leg.ALegID, BLegID: leg.BLegID, Seq: seq + 1,
			BackendID: leg.BackendID, ProviderID: leg.ProviderID, ModelID: leg.ModelID,
			StartedAt: leg.StartedAt, FinishedAt: leg.FinishedAt, Outcome: LegOutcome(leg.Outcome), Surfaced: leg.Surfaced,
			Evidence: leg.Evidence, OperatorRateRef: leg.OperatorRateRef,
		})
		legFingerprints = append(legFingerprints, leg.Fingerprint)
	}
	turn := TurnUsageRecord{
		SchemaVersion: CurrentRecordSchemaVersion, Key: turnKey, AccountID: call.AccountID,
		TurnID: call.CallID.String(), ALegID: call.ALegID, SessionID: call.SessionID,
		StartedAt: call.StartedAt, FinishedAt: call.FinishedAt, Outcome: call.Outcome,
		CustomerPricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef, Legs: legs,
	}
	if err := in.CustomerPricing.Validate(in.MaxCustomerCharge.Currency); err != nil {
		return CallRatingResult{}, err
	}
	ratingInput := RatingInput{Record: turn, CustomerPricing: in.CustomerPricing, CustomerPolicy: in.CustomerPolicy, OperatorRates: in.OperatorRates}
	customer, err := calculateCustomerCharge(turn, ratingInput)
	if err != nil {
		return CallRatingResult{}, err
	}
	fp, err := callRatingFingerprint(call, customer, in.MaxCustomerCharge, legFingerprints)
	if err != nil {
		return CallRatingResult{}, err
	}
	return CallRatingResult{CallID: call.CallID, CustomerCharge: customer, Fingerprint: fp}, nil
}

func containsExpectedLeg(ids []string, id string) bool {
	id = strings.TrimSpace(id)
	for _, candidate := range ids {
		if strings.TrimSpace(candidate) == id {
			return true
		}
	}
	return false
}

func callRatingFingerprint(call CallUsageRecord, amount Money, max Money, legFingerprints []string) (string, error) {
	sorted := append([]string(nil), legFingerprints...)
	sort.Strings(sorted)
	return fmt.Sprintf(
		"call-rating:v2:%s:%d:%s:max=%d:%s:pricing=%s@%s:policy=%s@%s:legs=%s",
		call.CallID.String(),
		amount.Nano,
		amount.Currency,
		max.Nano,
		max.Currency,
		call.CustomerPricingRef.ID,
		call.CustomerPricingRef.Version,
		call.ChargePolicyRef.ID,
		call.ChargePolicyRef.Version,
		strings.Join(sorted, ","),
	), nil
}
