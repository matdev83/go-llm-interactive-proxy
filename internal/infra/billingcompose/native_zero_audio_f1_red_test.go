package billingcompose_test

// F1 (PR #666 adversarial re-review): ordinary V2 input/output card plus an
// OpenAI-family provider observation that carries a present, directional ZERO
// native audio_token detail. The aggregate card is published with the real
// schema-free PutPricing path (not the opt-in PutPricingWithSchemas), the
// observation is produced by the real openaiusage native wire mapper, and the
// rating runs through the production customer resolver and V2 settlement
// boundary.
//
// A present exactly-zero quantity cannot attract any charge under any finite
// rate, so an unmatched zero detail must not make the retail valuation partial.
// This must stay distinct from a missing/absent/null detail (which carries no
// observed quantity and remains incomplete) and from a positive unmatched
// detail (which stays fail-closed until the tariff actually prices it).

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaiusage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	sdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const (
	f1StoreID     = "store-1"
	f1AccountID   = "acct-1"
	f1ALegID      = "a-1"
	f1BLegID      = "b-1"
	f1BackendID   = "backend"
	f1ModelID     = "model"
	f1InputTokens = 100
	f1OutputToken = 20
	f1TotalTokens = 120
)

// f1OpenAIWireObservation runs a synthetic OpenAI Chat usage payload through the
// production native wire mapper (openaiusage.ProviderEvidenceDraft) and the
// production evidence buffer, yielding the intact V2 observation the rater
// receives in production.
func f1OpenAIWireObservation(t *testing.T, callID billing.BillingCallID, raw string) sdkmetering.Observation {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	event := lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		InputTokens:   f1InputTokens,
		OutputTokens:  f1OutputToken,
		TotalTokens:   f1TotalTokens,
		UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
		RawUsageJSON:  raw,
		Accounting: lipapi.UsageAccountingMetadata{
			Source:    lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
	draft := openaiusage.ProviderEvidenceDraft(event, "openai.chat.v2", "f1:"+callID.String())
	buffer := coremetering.NewProviderEvidenceBuffer()
	buffer.BindEconomicEvidence(coremetering.ObservationIdentity{
		StoreID: f1StoreID, RequestID: "req-1", CallID: callID.String(), BillingCallID: callID.String(),
		ALegID: f1ALegID, BLegID: f1BLegID, AttemptID: f1BLegID, ObservedAt: now, ReceivedAt: now,
	})
	buffer.Add(draft)
	drained := buffer.DrainEconomicObservations()
	if len(drained) != 1 {
		t.Fatalf("drained observations=%d, want one intact observation", len(drained))
	}
	return drained[0]
}

func f1CompleteCall(t *testing.T, callID billing.BillingCallID, pricingRef, policyRef billing.VersionRef, obs sdkmetering.Observation) (billing.CompleteCall, billing.CallExposure) {
	t.Helper()
	now := time.Unix(100, 0).UTC()
	call := billing.CallUsageRecord{
		SchemaVersion:      billing.CurrentRecordSchemaVersion,
		CallID:             callID,
		AccountID:          f1AccountID,
		ALegID:             f1ALegID,
		StartedAt:          now,
		FinishedAt:         now.Add(time.Second),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: pricingRef,
		ChargePolicyRef:    policyRef,
		ExpectedBLegIDs:    []string{f1BLegID},
	}
	leg := billing.CallLegUsageRecord{
		CallID: callID, ALegID: f1ALegID, BLegID: f1BLegID, AttemptSeq: 1,
		BackendID: f1BackendID, ProviderID: "provider", ModelID: f1ModelID,
		StartedAt: now, FinishedAt: now.Add(time.Second),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		EvidenceVersion: billing.EvidenceFormatVersionV2, EvidenceProjection: billing.EvidenceProjectionV1,
		Observations: []sdkmetering.Observation{obs},
	}
	exposure := billing.CallExposure{
		AccountID: f1AccountID, CallID: callID.String(),
		Max:        billing.Money{Nano: 1_000_000, Currency: "USD"},
		Status:     billing.ExposureOpen,
		CreatedAt:  now,
		PricingRef: pricingRef, ChargePolicyRef: policyRef,
	}
	return billing.CompleteCall{Closure: call, Legs: []billing.CallLegUsageRecord{leg}}, exposure
}

func f1RateThroughResolver(t *testing.T, c *billingcompose.SnapshotCatalog, callID billing.BillingCallID, pricingRef, policyRef billing.VersionRef, obs sdkmetering.Observation) (billing.CallRatingResult, error) {
	t.Helper()
	resolver, err := billingcompose.NewCallRatingResolver(c)
	if err != nil {
		t.Fatalf("NewCallRatingResolver: %v", err)
	}
	aware, ok := resolver.(billing.OwnerAwareCallRatingResolver)
	if !ok {
		t.Fatalf("production resolver must implement OwnerAwareCallRatingResolver")
	}
	complete, exposure := f1CompleteCall(t, callID, pricingRef, policyRef, obs)
	return aware.ResolveCallRatingForOwner(context.Background(), complete, exposure, billing.PostingOwnerV2)
}

func f1ComponentLine(val economics.Valuation, component string) *economics.LineItem {
	for i := range val.Lines {
		line := &val.Lines[i]
		if line.Component == nil {
			continue
		}
		if line.Component.Component == component {
			return line
		}
	}
	return nil
}

func f1ObservationRetained(val economics.Valuation, obs sdkmetering.Observation) bool {
	want, err := obs.Ref(obs.Subject.StoreID)
	if err != nil {
		return false
	}
	for _, ref := range val.InputObservations {
		if ref.Equal(want) {
			return true
		}
	}
	return false
}

// TestF1OrdinaryZeroAudioCardSettlesV2 is the F1 RED vector. A schema-free
// ordinary input/output card (PutPricing) plus a present zero native
// audio_token detail must settle complete through the production V2 customer
// resolver and settlement boundary.
func TestF1OrdinaryZeroAudioCardSettlesV2(t *testing.T) {
	t.Parallel()
	c, pricing, policy, _ := seedCatalog(t)
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	const zeroAudio = `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"audio_tokens":0},"completion_tokens_details":{"audio_tokens":0}}`
	obs := f1OpenAIWireObservation(t, callID, zeroAudio)

	// The native mapper really retained directional observed zero audio.
	var zeroAudioMeasures int
	for _, measure := range obs.Measures {
		if measure.Key.Component == sdkmetering.ComponentAudioToken {
			zeroAudioMeasures++
			if measure.Value == nil || measure.Value.Coefficient != "0" {
				t.Fatalf("native audio measure=%+v, want present zero", measure)
			}
		}
	}
	if zeroAudioMeasures != 2 {
		t.Fatalf("directional zero audio measures=%d, want 2; measures=%+v", zeroAudioMeasures, obs.Measures)
	}

	result, err := f1RateThroughResolver(t, c, callID, pricing.Ref, policy.Ref, obs)
	if err != nil {
		t.Fatalf("ordinary zero-audio card must settle, got %v; lines=%+v", err, result.CustomerValuation.Lines)
	}
	if result.CustomerValuation.Completeness != economics.CompletenessComplete {
		t.Fatalf("completeness=%q, want complete; lines=%+v", result.CustomerValuation.Completeness, result.CustomerValuation.Lines)
	}
	if line := f1ComponentLine(result.CustomerValuation, sdkmetering.ComponentInputToken); line == nil || line.Amount == nil {
		t.Fatalf("input_token line missing: %+v", result.CustomerValuation.Lines)
	}
	if line := f1ComponentLine(result.CustomerValuation, sdkmetering.ComponentOutputToken); line == nil || line.Amount == nil {
		t.Fatalf("output_token line missing: %+v", result.CustomerValuation.Lines)
	}
	if line := f1ComponentLine(result.CustomerValuation, sdkmetering.ComponentAudioToken); line != nil && line.Status == economics.RatingLineRateMissing {
		t.Fatalf("zero audio must not be reported as a missing rate: %+v", line)
	}
	if !f1ObservationRetained(result.CustomerValuation, obs) {
		t.Fatalf("zero-audio observation reference must stay in the immutable input set")
	}
}

// TestF1OrdinaryAbsentAudioCardSettlesV2 is the absent-audio control: no audio
// detail is surfaced at all, so no native audio measure exists. The ordinary
// card must settle exactly as before.
func TestF1OrdinaryAbsentAudioCardSettlesV2(t *testing.T) {
	t.Parallel()
	c, pricing, policy, _ := seedCatalog(t)
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	const noAudio = `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}`
	obs := f1OpenAIWireObservation(t, callID, noAudio)
	for _, measure := range obs.Measures {
		if measure.Key.Component == sdkmetering.ComponentAudioToken {
			t.Fatalf("absent audio must produce no native audio measure: %+v", measure)
		}
	}
	result, err := f1RateThroughResolver(t, c, callID, pricing.Ref, policy.Ref, obs)
	if err != nil {
		t.Fatalf("absent-audio ordinary card must settle, got %v; lines=%+v", err, result.CustomerValuation.Lines)
	}
	if result.CustomerValuation.Completeness != economics.CompletenessComplete {
		t.Fatalf("absent-audio completeness=%q, want complete", result.CustomerValuation.Completeness)
	}
}

// TestF1UnpricedPositiveAudioStillFailsClosedV2 proves the zero carve-out does
// not weaken fail-closed behavior: a present positive native audio detail that
// the schema-free card does not price must remain partial.
func TestF1UnpricedPositiveAudioStillFailsClosedV2(t *testing.T) {
	t.Parallel()
	c, pricing, policy, _ := seedCatalog(t)
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	const positiveAudio = `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"audio_tokens":3},"completion_tokens_details":{"audio_tokens":4}}`
	obs := f1OpenAIWireObservation(t, callID, positiveAudio)
	if _, err := f1RateThroughResolver(t, c, callID, pricing.Ref, policy.Ref, obs); err == nil {
		t.Fatalf("unpriced positive native audio must stay fail-closed")
	}
}

// f1SeedSchemaCatalog publishes the ordinary card through the explicit
// schema-bearing publication path, binding the real OpenAI provider-family
// inclusion schema instead of the schema-free legacy default.
func f1SeedSchemaCatalog(t *testing.T) (*billingcompose.SnapshotCatalog, billing.PricingSnapshot, billing.ChargePolicy) {
	t.Helper()
	c := billingcompose.NewSnapshotCatalog()
	pricing := catalogPricing()
	policy := catalogPolicy()
	if err := c.PutPricingWithSchemas(pricing, openaiusage.NativeUsageInclusionSchemas()); err != nil {
		t.Fatalf("PutPricingWithSchemas: %v", err)
	}
	if err := c.PutPolicy(policy); err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}
	if err := c.SetDefaults(pricing.Ref, policy.Ref); err != nil {
		t.Fatalf("SetDefaults: %v", err)
	}
	// Identical replay stays idempotent; the frozen body is never rewritten.
	if err := c.PutPricingWithSchemas(pricing, openaiusage.NativeUsageInclusionSchemas()); err != nil {
		t.Fatalf("identical schema replay: %v", err)
	}
	return c, pricing, policy
}

// TestF1ExplicitNativeSchemaAudioSettlesV2 covers the deliberate explicit
// provider composition: the ordinary card published with the frozen OpenAI
// inclusion schema. A present native audio detail (zero or positive) is proven
// included in the priced aggregate and must settle without a separate charge.
func TestF1ExplicitNativeSchemaAudioSettlesV2(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
	}{
		{name: "zero", raw: `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"audio_tokens":0},"completion_tokens_details":{"audio_tokens":0}}`},
		{name: "positive", raw: `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"audio_tokens":3},"completion_tokens_details":{"audio_tokens":4}}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, pricing, policy := f1SeedSchemaCatalog(t)
			callID, err := billing.NewBillingCallID()
			if err != nil {
				t.Fatal(err)
			}
			obs := f1OpenAIWireObservation(t, callID, tc.raw)
			result, err := f1RateThroughResolver(t, c, callID, pricing.Ref, policy.Ref, obs)
			if err != nil {
				t.Fatalf("explicit native schema %s audio must settle, got %v; lines=%+v", tc.name, err, result.CustomerValuation.Lines)
			}
			if result.CustomerValuation.Completeness != economics.CompletenessComplete {
				t.Fatalf("explicit native schema %s audio completeness=%q, want complete", tc.name, result.CustomerValuation.Completeness)
			}
			if line := f1ComponentLine(result.CustomerValuation, sdkmetering.ComponentAudioToken); line != nil && line.Amount != nil && line.Amount.Coefficient != "0" {
				t.Fatalf("included native audio must not be independently payable: %+v", line)
			}
		})
	}
}

// TestF1RouteSpecificZeroAudioCardSettlesV2 covers a schema-free route override
// card resolved through the real route binding: the zero native audio detail
// must settle exactly like the default card.
func TestF1RouteSpecificZeroAudioCardSettlesV2(t *testing.T) {
	t.Parallel()
	c := billingcompose.NewSnapshotCatalog()
	def := catalogPricing()
	policy := catalogPolicy()
	if err := c.PutPricing(def); err != nil {
		t.Fatalf("PutPricing default: %v", err)
	}
	if err := c.PutPolicy(policy); err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}
	if err := c.SetDefaults(def.Ref, policy.Ref); err != nil {
		t.Fatalf("SetDefaults: %v", err)
	}
	override := catalogPricing()
	override.Ref = billing.VersionRef{ID: "pricing-route", Version: "v1"}
	if err := c.PutPricing(override); err != nil {
		t.Fatalf("PutPricing route: %v", err)
	}
	if err := c.SetRoutePricing(f1BackendID, f1ModelID, override.Ref); err != nil {
		t.Fatalf("SetRoutePricing: %v", err)
	}

	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	const zeroAudio = `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"audio_tokens":0},"completion_tokens_details":{"audio_tokens":0}}`
	obs := f1OpenAIWireObservation(t, callID, zeroAudio)
	result, err := f1RateThroughResolver(t, c, callID, def.Ref, policy.Ref, obs)
	if err != nil {
		t.Fatalf("route-specific zero-audio card must settle, got %v; lines=%+v", err, result.CustomerValuation.Lines)
	}
	if result.CustomerValuation.Completeness != economics.CompletenessComplete {
		t.Fatalf("route-specific zero-audio completeness=%q, want complete", result.CustomerValuation.Completeness)
	}
	if !f1ObservationRetained(result.CustomerValuation, obs) {
		t.Fatalf("route-specific zero-audio observation reference must stay retained")
	}
}

// TestF1ZeroAudioChargeMatchesAbsentAudioV2 proves the money effect of the fix
// directly: a present zero native audio detail contributes exactly nothing, so
// the settled customer charge is identical to the absent-audio control under
// the same ordinary schema-free card.
func TestF1ZeroAudioChargeMatchesAbsentAudioV2(t *testing.T) {
	t.Parallel()
	c, pricing, policy, _ := seedCatalog(t)
	zeroCallID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	absentCallID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	const zeroAudio = `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"audio_tokens":0},"completion_tokens_details":{"audio_tokens":0}}`
	const noAudio = `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}`
	zeroObs := f1OpenAIWireObservation(t, zeroCallID, zeroAudio)
	absentObs := f1OpenAIWireObservation(t, absentCallID, noAudio)

	zeroResult, err := f1RateThroughResolver(t, c, zeroCallID, pricing.Ref, policy.Ref, zeroObs)
	if err != nil {
		t.Fatalf("zero-audio settle: %v", err)
	}
	absentResult, err := f1RateThroughResolver(t, c, absentCallID, pricing.Ref, policy.Ref, absentObs)
	if err != nil {
		t.Fatalf("absent-audio settle: %v", err)
	}
	if zeroResult.CustomerCharge != absentResult.CustomerCharge {
		t.Fatalf("zero-audio charge %+v differs from absent-audio charge %+v", zeroResult.CustomerCharge, absentResult.CustomerCharge)
	}
}
