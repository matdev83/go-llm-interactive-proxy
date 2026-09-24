package runtimebundle

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	runtimecore "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
)

// Accounting recovery enforcement for Task 17.4 (Migration Strategy step 7,
// requirements 10.5, 17.5, 18.4).
//
// A process proves compatible accounting format/epoch-reader capability
// before serving: VerifyBillingAccountingRecovery loads the durable
// per-store snapshot and enforces the binary capability, failing startup
// with billing.ErrAccountingStaleBinary when the store carries V2 financial
// state the binary cannot read or fence. The check is read-only and runs on
// the billing-enabled startup path (buildProcessBillingRuntime); every
// non-nil internal store-backed composition must expose the snapshot port
// and fail closed when it is hidden, unreadable, malformed, or
// incompatible. The only path that skips verification carries no internal
// durable store (the public external monetary binding, which never starts
// store-backed workers here).
//
// When an operator must keep a process up without a compatible binary, the
// explicit alternative is NewQuiescedStrictAdmission: strict monetary
// admissions are denied deterministically with
// billing.ErrAccountingStrictQuiesced and zero new effects, until compatible
// recovery recomposes the process. There is no silent downgrade on either
// path.

// AccountingRecoveryStore is the narrow startup-verification port. Every
// durable billing store in an internal store-backed monetary composition
// implements it; production doubles carry an explicit safe snapshot. A
// non-nil store that hides this port is rejected at startup (fail closed)
// instead of silently serving unverified financial state.
type AccountingRecoveryStore interface {
	GetAccountingRecoverySnapshot(context.Context) (billing.AccountingRecoverySnapshot, error)
}

// VerifyBillingAccountingRecovery loads the durable snapshot for store and
// enforces capability before the process admits or posts anything.
// Compatible binaries receive the snapshot; stale binaries receive
// billing.ErrAccountingStaleBinary. The check is read-only.
func VerifyBillingAccountingRecovery(ctx context.Context, store AccountingRecoveryStore, capability billing.AccountingBinaryCapability) (billing.AccountingRecoverySnapshot, error) {
	if ctx == nil {
		return billing.AccountingRecoverySnapshot{}, fmt.Errorf("runtimebundle: nil context for accounting recovery verification")
	}
	if billing.IsNilPort(store) {
		return billing.AccountingRecoverySnapshot{}, fmt.Errorf("%w: accounting recovery store is required", ErrComposeBillingIncomplete)
	}
	if err := capability.Validate(); err != nil {
		return billing.AccountingRecoverySnapshot{}, err
	}
	snapshot, err := store.GetAccountingRecoverySnapshot(ctx)
	if err != nil {
		return billing.AccountingRecoverySnapshot{}, err
	}
	if err := billing.CheckAccountingStartup(snapshot, capability); err != nil {
		return billing.AccountingRecoverySnapshot{}, err
	}
	return snapshot, nil
}

// verifyBillingAccountingStartup enforces the current binary capability on
// the billing-enabled startup path. A nil store carries no durable state
// and keeps prior behavior (stock host / external binding without an
// internal store never reaches here with a store). Any non-nil
// internal store-backed composition must expose AccountingRecoveryStore;
// a decorator hiding the port fails closed here before any worker or sink
// starts. Snapshot-capable stores fail closed when unreadable, malformed,
// or when the durable state requires a different capability.
func verifyBillingAccountingStartup(ctx context.Context, store billing.AuthoritativeBilling) error {
	if billing.IsNilPort(store) {
		return nil
	}
	snapshotter, ok := store.(AccountingRecoveryStore)
	if !ok || billing.IsNilPort(snapshotter) {
		return fmt.Errorf("%w: accounting recovery store port is required for internal store-backed billing composition",
			ErrComposeBillingIncomplete)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := VerifyBillingAccountingRecovery(ctx, snapshotter, billing.CurrentAccountingBinaryCapability())
	return err
}

// QuiescedStrictAdmission is the explicit quiesce alternative to failing
// startup: while the frozen durable snapshot requires strict quiesce for the
// frozen binary capability, Admit fails deterministically with
// billing.ErrAccountingStrictQuiesced before reaching the inner admission
// (zero new effects: no exposures, pins, journals, or balance moves).
// Otherwise it delegates to the inner admission unchanged. The verdict is
// frozen at construction from explicit inputs so admissions stay
// deterministic for the process lifetime; compatible recovery recomposes
// the process with a fresh snapshot.
type QuiescedStrictAdmission struct {
	inner    runtimecore.BillingExposureAdmission
	snapshot billing.AccountingRecoverySnapshot
	quiesced bool
}

// NewQuiescedStrictAdmission composes the quiesce wrapper over inner.
// It fails closed on nil inner admission, invalid snapshots, or invalid
// capabilities.
func NewQuiescedStrictAdmission(inner runtimecore.BillingExposureAdmission, snapshot billing.AccountingRecoverySnapshot, capability billing.AccountingBinaryCapability) (*QuiescedStrictAdmission, error) {
	if billing.IsNilPort(inner) {
		return nil, fmt.Errorf("%w: quiesced admission inner is required", ErrComposeBillingIncomplete)
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	if err := capability.Validate(); err != nil {
		return nil, err
	}
	return &QuiescedStrictAdmission{
		inner:    inner,
		snapshot: snapshot,
		quiesced: billing.AccountingRequiresStrictQuiesce(snapshot, capability),
	}, nil
}

// Quiesced reports whether strict monetary admissions are quiesced.
func (q *QuiescedStrictAdmission) Quiesced() bool {
	return q != nil && q.quiesced
}

// Admit denies with billing.ErrAccountingStrictQuiesced while quiesced and
// delegates to the inner admission otherwise.
func (q *QuiescedStrictAdmission) Admit(ctx context.Context, in runtimecore.BillingExposureAdmissionInput) (billing.CallExposure, error) {
	if q == nil || billing.IsNilPort(q.inner) {
		return billing.CallExposure{}, fmt.Errorf("%w: quiesced admission is not composed", ErrComposeBillingIncomplete)
	}
	if q.quiesced {
		return billing.CallExposure{}, fmt.Errorf("%w: store %q requires a compatible reader",
			billing.ErrAccountingStrictQuiesced, q.snapshot.StoreID)
	}
	return q.inner.Admit(ctx, in)
}

var _ runtimecore.BillingExposureAdmission = (*QuiescedStrictAdmission)(nil)
