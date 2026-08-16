package billingcompose_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
)

func TestNewCallRatingResolver(t *testing.T) {
	t.Parallel()
	t.Run("nil catalog", func(t *testing.T) {
		t.Parallel()
		got, err := billingcompose.NewCallRatingResolver(nil)
		if err == nil || got != nil {
			t.Fatalf("got=%v err=%v, want error", got, err)
		}
	})
	t.Run("success", func(t *testing.T) {
		t.Parallel()
		c, _, _, _ := seedCatalog(t)
		got, err := billingcompose.NewCallRatingResolver(c)
		if err != nil || got == nil {
			t.Fatalf("got=%v err=%v", got, err)
		}
	})
}

func TestProviderCostJoinResolverRatesOnlyOnFallback(t *testing.T) {
	t.Parallel()
	missingRateRef := billing.VersionRef{ID: "operator-rates", Version: "missing"}

	t.Run("authoritative cost without catalog rate", func(t *testing.T) {
		t.Parallel()
		resolver, err := billingcompose.NewProviderCostResolver(billingcompose.NewSnapshotCatalog(), "USD")
		if err != nil {
			t.Fatal(err)
		}
		leg := providerCostTestLeg(t, missingRateRef, billing.FinalBillingEvidence{
			Cost:      billing.MoneyEvidence{NanoUnits: 42, Currency: "USD", Present: true},
			Source:    billing.EvidenceSourceProviderReported,
			Authority: billing.EvidenceAuthorityAuthoritative,
			DedupeKey: "auth-cost",
		})
		got, err := resolver.ResolveProviderCost(context.Background(), leg)
		if err != nil {
			t.Fatalf("ResolveProviderCost: %v", err)
		}
		if !got.Authoritative || !got.Reconciled || got.Amount.Nano != 42 {
			t.Fatalf("authoritative result = %+v", got)
		}
	})

	t.Run("rejected leg without catalog rate is reconciled zero", func(t *testing.T) {
		t.Parallel()
		resolver, err := billingcompose.NewProviderCostResolver(billingcompose.NewSnapshotCatalog(), "USD")
		if err != nil {
			t.Fatal(err)
		}
		leg := providerCostTestLeg(t, missingRateRef, billing.FinalBillingEvidence{})
		leg.Outcome = billing.LegOutcomeRejected
		got, err := resolver.ResolveProviderCost(context.Background(), leg)
		if err != nil {
			t.Fatalf("ResolveProviderCost: %v", err)
		}
		if !got.Reconciled || got.Amount.Nano != 0 || !got.AmountPresent {
			t.Fatalf("rejected zero = %+v", got)
		}
	})

	t.Run("accepted tokens without catalog rate stay unreconciled", func(t *testing.T) {
		t.Parallel()
		resolver, err := billingcompose.NewProviderCostResolver(billingcompose.NewSnapshotCatalog(), "USD")
		if err != nil {
			t.Fatal(err)
		}
		leg := providerCostTestLeg(t, missingRateRef, billing.FinalBillingEvidence{
			InputTokens:  billing.Quantity{Value: 1_000_000, Present: true},
			OutputTokens: billing.Quantity{Value: 1_000_000, Present: true},
		})
		_, err = resolver.ResolveProviderCost(context.Background(), leg)
		if !errors.Is(err, billing.ErrUnreconciledCost) {
			t.Fatalf("error = %v, want %v", err, billing.ErrUnreconciledCost)
		}
	})

	t.Run("accepted tokens with published rate", func(t *testing.T) {
		t.Parallel()
		c, _, _, rates := seedCatalog(t)
		resolver, err := billingcompose.NewProviderCostResolver(c, "USD")
		if err != nil {
			t.Fatal(err)
		}
		leg := providerCostTestLeg(t, rates.Ref, billing.FinalBillingEvidence{
			InputTokens:  billing.Quantity{Value: 1_000_000, Present: true},
			OutputTokens: billing.Quantity{Value: 1_000_000, Present: true},
		})
		got, err := resolver.ResolveProviderCost(context.Background(), leg)
		if err != nil {
			t.Fatalf("ResolveProviderCost: %v", err)
		}
		want := rates.InputPerMillionNano + rates.OutputPerMillionNano
		if !got.Reconciled || got.Authoritative || got.Amount.Nano != want {
			t.Fatalf("rated result = %+v, want nano=%d", got, want)
		}
	})
}

func providerCostTestLeg(t *testing.T, rateRef billing.VersionRef, evidence billing.FinalBillingEvidence) billing.CallLegUsageRecord {
	t.Helper()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	return billing.CallLegUsageRecord{
		CallID: callID, ALegID: "a-1", BLegID: "b-1",
		BackendID: "backend", ProviderID: "provider", ModelID: "model",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		Evidence: evidence, OperatorRateRef: rateRef,
	}
}
