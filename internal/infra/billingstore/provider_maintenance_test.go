package billingstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestAppendProviderMaintenanceIsDurableAndReplaySafe(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	usage := billing.ProviderMaintenanceUsage{
		OperationID: "maintenance-operation-1",
		ALegID:      "a-leg-1",
		TargetID:    "target-1",
		BackendID:   "anthropic",
		ModelID:     "claude",
		RecordedAt:  time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
		Evidence: billing.FinalBillingEvidence{
			InputTokens: billing.Quantity{Value: 11, Present: true},
			Source:      billing.EvidenceSourceProviderReported,
			Authority:   billing.EvidenceAuthorityAuthoritative,
			DedupeKey:   "maintenance-operation-1",
		},
	}
	if err := store.AppendProviderMaintenance(ctx, usage); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendProviderMaintenance(ctx, usage); err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	var count int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM provider_maintenance_usage WHERE operation_id = ?`, usage.OperationID).Scan(ctx, &count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("durable maintenance rows = %d, want 1", count)
	}

	usage.Evidence.InputTokens.Value++
	if err := store.AppendProviderMaintenance(ctx, usage); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("conflicting replay = %v, want identity conflict", err)
	}
}
