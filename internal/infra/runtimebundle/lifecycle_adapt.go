package runtimebundle

import (
	"context"
	"errors"
	"fmt"

	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
)

// ErrUnsafeLifecycleOverlap is returned when a feature/plugin lifecycle cannot
// safely Start/Stop under candidate overlap (req 8.8, design lifecycle adapt).
var ErrUnsafeLifecycleOverlap = errors.New("runtimebundle: lifecycle change cannot overlap safely")

// ErrCandidateFaultInjected is returned when TestingOptions injects a candidate fault.
var ErrCandidateFaultInjected = errors.New("runtimebundle: candidate fault injected")

// CandidateOverlapSafe marks a Lifecycle that may Start while another generation
// remains active. Unmarked Lifecycles are rejected before publication.
type CandidateOverlapSafe interface {
	SafeUnderCandidateOverlap() bool
}

// ClassifyFeatureLifecycles rejects lifecycles that are not explicitly overlap-safe.
func ClassifyFeatureLifecycles(lifes []lipplugin.Lifecycle) error {
	for i, life := range lifes {
		if life == nil {
			continue
		}
		safe, ok := life.(CandidateOverlapSafe)
		if !ok || !safe.SafeUnderCandidateOverlap() {
			return fmt.Errorf("%w: index=%d type=%T", ErrUnsafeLifecycleOverlap, i, life)
		}
	}
	return nil
}

// AdaptOverlapSafeLifecycles registers Start on prepare and Stop on close/rollback
// for lifecycles already classified as overlap-safe.
func AdaptOverlapSafeLifecycles(ledger *ResourceLedger, lifes []lipplugin.Lifecycle) error {
	if ledger == nil {
		return fmt.Errorf("runtimebundle: nil resource ledger")
	}
	if err := ClassifyFeatureLifecycles(lifes); err != nil {
		return err
	}
	for i, life := range lifes {
		if life == nil {
			continue
		}
		life := life
		name := fmt.Sprintf("feature-lifecycle-%d", i)
		ledger.AddAction(name, PhasePrepare,
			func(ctx context.Context) error { return life.Start(ctx) },
			func(ctx context.Context) error { return life.Stop(ctx) },
		)
	}
	return nil
}
