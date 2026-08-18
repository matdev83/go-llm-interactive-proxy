package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ProviderMaintenanceUsage is provider-authoritative usage from a control-plane
// maintenance call. It is intentionally not a CallLegUsageRecord: maintenance
// must retain its operation identity without masquerading as a foreground B-leg.
type ProviderMaintenanceUsage struct {
	OperationID string
	ALegID      string
	TargetID    string
	BackendID   string
	ModelID     string
	RecordedAt  time.Time
	Evidence    FinalBillingEvidence
}

func (u ProviderMaintenanceUsage) Validate() error {
	if strings.TrimSpace(u.OperationID) == "" || strings.TrimSpace(u.ALegID) == "" ||
		strings.TrimSpace(u.TargetID) == "" || strings.TrimSpace(u.BackendID) == "" ||
		strings.TrimSpace(u.ModelID) == "" || u.RecordedAt.IsZero() {
		return fmt.Errorf("%w: maintenance identity and timestamp are required", ErrInvalidRecord)
	}
	if err := validateEvidence(u.Evidence); err != nil {
		return err
	}
	return nil
}

func (u ProviderMaintenanceUsage) Fingerprint() (string, error) {
	if err := u.Validate(); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(struct {
		Version     string
		OperationID string
		ALegID      string
		TargetID    string
		BackendID   string
		ModelID     string
		Evidence    FinalBillingEvidence
	}{
		Version: "provider-maintenance:v1", OperationID: u.OperationID, ALegID: u.ALegID,
		TargetID: u.TargetID, BackendID: u.BackendID, ModelID: u.ModelID, Evidence: u.Evidence,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "provider-maintenance:v1:" + hex.EncodeToString(digest[:]), nil
}

// ProviderMaintenanceUsageStore is the durable provider-billable persistence
// boundary for control-plane maintenance usage.
type ProviderMaintenanceUsageStore interface {
	AppendProviderMaintenance(context.Context, ProviderMaintenanceUsage) error
}

// ProviderMaintenanceUsageObserver delivers immutable maintenance evidence to
// its durable owner. Errors stay visible to the keep-warm lifecycle so delivery
// failures are not silently mistaken for successful accounting.
type ProviderMaintenanceUsageObserver interface {
	ObserveProviderMaintenance(context.Context, ProviderMaintenanceUsage) error
}

type ProviderMaintenanceUsageObserverFunc func(context.Context, ProviderMaintenanceUsage) error

func (f ProviderMaintenanceUsageObserverFunc) ObserveProviderMaintenance(ctx context.Context, usage ProviderMaintenanceUsage) error {
	if f == nil {
		return nil
	}
	return f(ctx, usage)
}
