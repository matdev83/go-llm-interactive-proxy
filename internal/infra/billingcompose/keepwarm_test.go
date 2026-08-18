package billingcompose_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/keepwarm"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

func TestKeepwarmHooksDeliverProviderMaintenanceUsage(t *testing.T) {
	t.Parallel()

	inputTokens := int64(11)
	outputTokens := int64(7)
	cacheReadTokens := int64(101)
	cacheWriteTokens := int64(13)
	reasoningTokens := int64(5)
	totalTokens := int64(137)
	observerCalls := 0
	var observedContext context.Context
	var observed billing.ProviderMaintenanceUsage

	hooks := billingcompose.KeepwarmHooks(billing.ProviderMaintenanceUsageObserverFunc(func(ctx context.Context, usage billing.ProviderMaintenanceUsage) error {
		observerCalls++
		observedContext = ctx
		observed = usage
		return nil
	}))
	if hooks.Accounting == nil {
		t.Fatal("KeepwarmHooks did not install an accounting callback")
	}

	before := time.Now().UTC()
	if err := hooks.Accounting(context.Background(), keepwarm.RenewalRecord{
		OperationID: "keepwarm:1:2:3",
		ALegID:      "a-leg-1",
		TargetID:    promptcache.TargetID("target-1"),
		BackendID:   "anthropic",
		ModelID:     "claude-code",
		Accounting: &promptcache.AccountingEvidence{
			InputTokens:      &inputTokens,
			OutputTokens:     &outputTokens,
			CacheReadTokens:  &cacheReadTokens,
			CacheWriteTokens: &cacheWriteTokens,
			ReasoningTokens:  &reasoningTokens,
			TotalTokens:      &totalTokens,
			Presence: lipapi.UsagePresence{
				InputTokens: true, OutputTokens: true, CacheReadTokens: true,
				CacheWriteTokens: true, ReasoningTokens: true, TotalTokens: true,
			},
			Source:    promptcache.AccountingSourceProviderReported,
			Authority: promptcache.AccountingAuthorityAuthoritative,
			Plane:     promptcache.AccountingPlaneProviderBillable,
			DedupeKey: "provider-call-1",
		},
	}); err != nil {
		t.Fatalf("accounting callback: %v", err)
	}
	after := time.Now().UTC()

	if observerCalls != 1 {
		t.Fatalf("observer calls = %d, want 1", observerCalls)
	}
	if observedContext == nil {
		t.Fatal("observer received nil context")
	}
	if observed.OperationID != "keepwarm:1:2:3" || observed.ALegID != "a-leg-1" ||
		observed.TargetID != "target-1" || observed.BackendID != "anthropic" || observed.ModelID != "claude-code" {
		t.Fatalf("maintenance lineage = %+v", observed)
	}
	if observed.RecordedAt.Before(before) || observed.RecordedAt.After(after) || observed.RecordedAt.Location() != time.UTC {
		t.Fatalf("RecordedAt = %v, want UTC timestamp during delivery", observed.RecordedAt)
	}
	got := observed.Evidence
	if got.InputTokens != (billing.Quantity{Value: inputTokens, Present: true}) ||
		got.OutputTokens != (billing.Quantity{Value: outputTokens, Present: true}) ||
		got.CacheReadTokens != (billing.Quantity{Value: cacheReadTokens, Present: true}) ||
		got.CacheWriteTokens != (billing.Quantity{Value: cacheWriteTokens, Present: true}) ||
		got.ReasoningTokens != (billing.Quantity{Value: reasoningTokens, Present: true}) ||
		got.TotalTokens != (billing.Quantity{Value: totalTokens, Present: true}) {
		t.Fatalf("mapped token evidence = %+v", got)
	}
	if got.Cost.Present || got.Source != billing.EvidenceSourceProviderReported ||
		got.Authority != billing.EvidenceAuthorityAuthoritative || got.DedupeKey != "keepwarm:1:2:3" {
		t.Fatalf("mapped billing metadata = %+v", got)
	}
}

func TestKeepwarmHooksSkipRenewalsWithoutAccountingEvidence(t *testing.T) {
	t.Parallel()

	var calls int
	hooks := billingcompose.KeepwarmHooks(billing.ProviderMaintenanceUsageObserverFunc(func(context.Context, billing.ProviderMaintenanceUsage) error {
		calls++
		return nil
	}))
	if err := hooks.Accounting(context.Background(), keepwarm.RenewalRecord{OperationID: "without-evidence"}); err != nil {
		t.Fatalf("accounting callback without evidence: %v", err)
	}
	if calls != 0 {
		t.Fatalf("observer calls = %d, want 0", calls)
	}
	if hooksWithoutObserver := billingcompose.KeepwarmHooks(nil); hooksWithoutObserver.Accounting != nil {
		t.Fatal("nil observer must not install an accounting callback")
	}
}
