package billing

import (
	"errors"
	"fmt"
	"strings"
)

// Durable cutover coordinator domain for Task 17.3 subtask B2a (Migration
// Strategy step 6).
//
// The coordinator enters draining, classifies/pins all existing V1 in-flight
// financial operations, prevents new unpinned V1 work, and permits claims
// only for correctly pinned V1 work. Activation proves V1 in-flight is
// drained/completed/classified before v2_active. V2 new work is authorized
// only in v2_active.
//
// Namespaces reuse existing canonical identities, never weak caller strings:
//   - customer call settlement: CustomerSettlementSourceKey(account, call)
//     from usage_call_records pending/leased rows.
//   - provider charge/payable: ProviderCostSourceKey(CallLegUsageKey(call,
//     bLeg) [+ ":provider-charge:"+chargeID]) from provider_cost_work pending
//     rows joined to their sealed legs.
//   - financial adjustment: F4 empty inventory. Synchronous adjustments have no
//     durable pending queue; only already-existing incomplete
//     financial_adjustment pins count. Historical heads are projections, never
//     pending work.
//
// No SQL, no provider SDKs, no globals/DI; small consumer-owned contracts
// only. Posting-time worker enforcement belongs to B2b; crash/lease tests
// belong to C. This file exposes only gates, eligibility, bounded config, and
// narrow claim metadata B2b consumes.

var (
	ErrCutoverCoordinatorInvalid = errors.New("billing: invalid cutover coordinator")
	ErrCutoverDrainBlocked       = errors.New("billing: cutover drain blocked")
	ErrCutoverV1Fenced           = errors.New("billing: V1 financial work fenced by cutover")
	ErrCutoverV2NotAuthorized    = errors.New("billing: V2 new work not authorized")
)

const (
	CutoverCoordinatorMinBatchSize             = 1
	CutoverCoordinatorMaxBatchSize             = 1000
	CutoverCoordinatorDefaultBatchSize         = 100
	CutoverCoordinatorDefaultMaxBatches        = 100
	CutoverCoordinatorMaxUnclassifiableSamples = 32
)

// CutoverCoordinatorConfig bounds draining readers. BatchSize bounds one
// deterministic page; MaxBatches bounds one Classify call so a huge backlog
// cannot hold a transaction open.
type CutoverCoordinatorConfig struct {
	BatchSize  int
	MaxBatches int
}

// NewCutoverCoordinatorConfig validates bounded reader configuration.
func NewCutoverCoordinatorConfig(batchSize int) (CutoverCoordinatorConfig, error) {
	if batchSize < CutoverCoordinatorMinBatchSize || batchSize > CutoverCoordinatorMaxBatchSize {
		return CutoverCoordinatorConfig{}, fmt.Errorf("%w: %w: batch size %d must be within [%d,%d]",
			ErrCutoverCoordinatorInvalid, ErrInvalidRecord, batchSize, CutoverCoordinatorMinBatchSize, CutoverCoordinatorMaxBatchSize)
	}
	return CutoverCoordinatorConfig{BatchSize: batchSize, MaxBatches: CutoverCoordinatorDefaultMaxBatches}, nil
}

// CutoverUnclassifiableItem records one operation that cannot be safely
// classified. The coordinator never invents completion for these; the marker
// remains draining with this explicit status.
type CutoverUnclassifiableItem struct {
	Namespace string
	Key       string
	Reason    string
}

// CutoverDrainCounts is the durable precondition snapshot for activation.
type CutoverDrainCounts struct {
	CustomerPending   int
	ProviderPending   int
	AdjustmentPending int
	V1Pinned          int
	V1Completed       int
	Unclassifiable    int
	// OpenExposures counts admitted call_exposures rows with status='open'.
	// F2A: admitted/open V1 calls survive drain; activation cannot succeed
	// while an admitted exposure remains open, even when no closure/leg row
	// or pin exists yet. Closed via terminal settlement (or legitimate
	// cancellation outcome through the same settlement seam); never invented.
	OpenExposures int
	// EconomicProviderPending counts pending/leased monetary provider economic
	// revision work (F2B). Evidence-only customer rating, reconciliation jobs,
	// shadow observations/valuations, and queues without a posting adapter are
	// never counted here.
	EconomicProviderPending int
}

// CutoverDrainStatus is the explicit draining result. ReadyForActivation is
// true only when no pending/leased/uncompleted V1 pins/work remain across all
// three namespaces and no unclassifiable items exist.
type CutoverDrainStatus struct {
	State              AccountingCutoverState
	MarkerVersion      uint64
	MarkerEpoch        uint64
	Counts             CutoverDrainCounts
	Unclassifiable     []CutoverUnclassifiableItem
	ReadyForActivation bool
}

// IsV1FinancialWorkAllowed reports whether ordinary (unpinned) V1
// append/admission/queue creation is allowed. v1_active/v2_shadow allow new V1
// with legacy defaults preserved; v1_draining/v2_active fence new V1.
// Historical replay of already pinned/completed V1 is handled separately via
// B1 replay paths, not this gate.
func IsV1FinancialWorkAllowed(state AccountingCutoverState) bool {
	switch state {
	case AccountingCutoverV1Active, AccountingCutoverV2Shadow:
		return true
	case AccountingCutoverV1Draining, AccountingCutoverV2Active:
		return false
	default:
		return false
	}
}

// IsV2NewWorkAuthorized reports whether explicit V2-new-work authorization
// holds. Only v2_active authorizes V2 posting admissions/new pins. Shadow
// capture remains no-post and unaffected (it does not consult this gate).
func IsV2NewWorkAuthorized(state AccountingCutoverState) bool {
	return state == AccountingCutoverV2Active
}

// IsV1ClaimEligible reports whether a V1 worker claim may proceed under state
// for pin. Pre-drain states preserve ordinary V1 claims without pin gating.
// Draining returns only correctly V1-pinned work. v2_active permits no V1
// claim (V1 completed-history replay flows through B1 pin replay, not worker
// claims).
func IsV1ClaimEligible(state AccountingCutoverState, pin PostingPin) bool {
	switch state {
	case AccountingCutoverV1Active, AccountingCutoverV2Shadow:
		return true
	case AccountingCutoverV1Draining:
		return pin.Owner == PostingOwnerV1
	case AccountingCutoverV2Active:
		return false
	default:
		return false
	}
}

// CutoverClaimMetadata is the narrow claim eligibility record B2b consumes for
// posting-time worker checks (including leases waking after an epoch change).
// It carries the expected owner/epoch/state needed to fence stale workers
// without re-reading the full pin.
//
// R4 mandatory binding: for economic revision leases the token additionally
// carries the work identity (WorkID) and the lease fence (LeaseOwner/Fence)
// issued atomically with the lease. Posting validates the fence against
// current delivery state before any monetary effect so a reclaimed lease
// fences the stale token. Customer/provider tokens leave these empty; any
// partially populated lease binding fails closed as malformed.
type CutoverClaimMetadata struct {
	Kind          PostingOperationKind
	OperationKey  string
	AccountID     string
	CallID        BillingCallID
	Owner         string
	MarkerVersion uint64
	MarkerEpoch   uint64
	MarkerState   AccountingCutoverState
	// WorkID is the economic revision work identity (identity.Key()) for
	// monetary economic leases; empty for customer/provider tokens.
	WorkID string
	// LeaseOwner is the economic lease owner bound to this token; empty for
	// customer/provider tokens.
	LeaseOwner string
	// LeaseFence is the economic lease fence bound to this token; zero for
	// customer/provider tokens.
	LeaseFence uint64
}

// CutoverClaimMetadataForPin derives the narrow B2b claim record from a
// durable pin.
func CutoverClaimMetadataForPin(pin PostingPin) CutoverClaimMetadata {
	return CutoverClaimMetadata{
		Kind:          pin.Kind,
		OperationKey:  pin.OperationKey,
		AccountID:     pin.AccountID,
		CallID:        pin.CallID,
		Owner:         pin.Owner,
		MarkerVersion: pin.MarkerVersion,
		MarkerEpoch:   pin.MarkerEpoch,
		MarkerState:   pin.MarkerState,
	}
}

// Validate fails closed on malformed claim metadata. Direct-adjustment pins
// (B2b4, "financial-adjustment-direct:v1:") carry no call lineage, so empty
// CallID is canonical there; all other namespaces require a valid call.
// Lease binding (WorkID/LeaseOwner/LeaseFence) must be all-present or
// all-absent; any partial binding fails closed as malformed.
func (m CutoverClaimMetadata) Validate() error {
	if !m.Kind.Valid() {
		return fmt.Errorf("%w: %w: unknown operation kind %q", ErrCutoverCoordinatorInvalid, ErrInvalidRecord, string(m.Kind))
	}
	if strings.TrimSpace(m.OperationKey) == "" {
		return fmt.Errorf("%w: %w: operation key is required", ErrCutoverCoordinatorInvalid, ErrInvalidRecord)
	}
	if strings.TrimSpace(m.OperationKey) != m.OperationKey {
		return fmt.Errorf("%w: %w: operation key must be trimmed", ErrCutoverCoordinatorInvalid, ErrInvalidRecord)
	}
	if m.Owner != PostingOwnerV1 && m.Owner != PostingOwnerV2 {
		return fmt.Errorf("%w: %w: unknown claim owner %q", ErrCutoverCoordinatorInvalid, ErrInvalidRecord, m.Owner)
	}
	if m.MarkerVersion == 0 || m.MarkerEpoch == 0 {
		return fmt.Errorf("%w: %w: claim marker version/epoch out of range", ErrCutoverCoordinatorInvalid, ErrInvalidRecord)
	}
	if !m.MarkerState.Valid() {
		return fmt.Errorf("%w: %w: unknown claim marker state %q", ErrCutoverCoordinatorInvalid, ErrInvalidRecord, string(m.MarkerState))
	}
	if err := validateCutoverLeaseBinding(m); err != nil {
		return err
	}
	if m.Kind == PostingOperationFinancialAdjustment && IsDirectAdjustmentPinKey(m.OperationKey) {
		if strings.TrimSpace(m.CallID.String()) != "" {
			return fmt.Errorf("%w: %w: direct adjustment claim must carry no call", ErrCutoverCoordinatorInvalid, ErrInvalidRecord)
		}
		return nil
	}
	if err := m.CallID.Validate(); err != nil {
		return fmt.Errorf("%w: %w: %v", ErrCutoverCoordinatorInvalid, ErrInvalidRecord, err)
	}
	return nil
}

// validateCutoverLeaseBinding fails closed on partially populated lease
// binding. Customer/provider tokens carry none; monetary economic tokens
// carry all three (work identity plus lease owner/fence).
func validateCutoverLeaseBinding(m CutoverClaimMetadata) error {
	workEmpty := strings.TrimSpace(m.WorkID) == ""
	ownerEmpty := strings.TrimSpace(m.LeaseOwner) == ""
	fenceEmpty := m.LeaseFence == 0
	if workEmpty && ownerEmpty && fenceEmpty {
		return nil
	}
	if workEmpty || ownerEmpty || fenceEmpty {
		return fmt.Errorf("%w: %w: cutover lease binding requires work, owner and fence together",
			ErrCutoverCoordinatorInvalid, ErrInvalidRecord)
	}
	if strings.TrimSpace(m.WorkID) != m.WorkID || strings.ContainsAny(m.WorkID, "\x00\r\n") {
		return fmt.Errorf("%w: %w: cutover work identity must be trimmed", ErrCutoverCoordinatorInvalid, ErrInvalidRecord)
	}
	if strings.TrimSpace(m.LeaseOwner) != m.LeaseOwner || strings.ContainsAny(m.LeaseOwner, "\x00\r\n") {
		return fmt.Errorf("%w: %w: cutover lease owner must be trimmed", ErrCutoverCoordinatorInvalid, ErrInvalidRecord)
	}
	if m.LeaseFence > 9223372036854775807 {
		return fmt.Errorf("%w: %w: cutover lease fence out of range", ErrCutoverCoordinatorInvalid, ErrInvalidRecord)
	}
	return nil
}
