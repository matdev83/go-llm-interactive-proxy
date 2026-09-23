package billing

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Durable accounting cutover marker for Task 17.3 foundation subtask A
// (Migration Strategy step 6).
//
// The marker is per configured deployment/store boundary (store_id primary
// key), never a global. It records the explicit accounting generation, the
// monotonic version/epoch used for compare-and-swap fencing, the active
// posting owner (V1 or V2 writer lineage), and the compatibility floor needed
// for later claim fencing (17.3B) and rollback checks (17.4). This file is the
// domain contract only: no SQL, no provider SDKs.
//
// State machine (strictly linear, monotonic):
//
//	v1_active   -> v2_shadow    (V2 capture/rating persists, V1 remains sole monetary writer)
//	v2_shadow   -> v1_draining  (stop old claims, drain/classify V1 in-flight, pin rating owner)
//	v1_draining -> v2_active    (V2 admissions enabled, V2 is the posting authority)
//
// Skipped, backward, same-state, and unknown-state transitions are invalid.
// Stale expected version/epoch is a fence conflict. Exact replay (same
// expected version/epoch, same target state, same transition identity) is
// idempotent and detected via IsAccountingCutoverReplay; the store layer
// performs the durable CAS. Terminal claim/worker/admission wiring belongs to
// later 17.3B/C work orders and must not be added here.

type AccountingCutoverState string

const (
	AccountingCutoverV1Active   AccountingCutoverState = "v1_active"
	AccountingCutoverV2Shadow   AccountingCutoverState = "v2_shadow"
	AccountingCutoverV1Draining AccountingCutoverState = "v1_draining"
	AccountingCutoverV2Active   AccountingCutoverState = "v2_active"
)

const (
	AccountingCutoverGenerationV1 = 1
	AccountingCutoverGenerationV2 = 2
)

// Posting owners reuse the durable writer lineage so later claim fencing can
// compare against the same ownership vocabulary used by historical readers.
const (
	AccountingPostingOwnerV1 = HistoricalV1WriterVersion
	AccountingPostingOwnerV2 = V2WriterVersion
)

// Compatibility floors record the minimum reader/binary generation that may
// operate against the store. v1_active/v2_shadow/v1_draining remain readable
// by V1-aware binaries; v2_active requires V2-aware readers. Rollback policy
// (17.4) consumes this field but is not implemented here.
const (
	AccountingCompatFloorV1 = HistoricalV1WriterVersion
	AccountingCompatFloorV2 = V2WriterVersion
)

const (
	accountingCutoverMaxStoreIDLen      = 256
	accountingCutoverMaxTransitionIDLen = 128
	accountingCutoverInitTransitionID   = "init:v1_active"
)

var (
	ErrAccountingCutoverInvalid  = errors.New("billing: invalid accounting cutover")
	ErrAccountingCutoverFence    = errors.New("billing: accounting cutover fence conflict")
	ErrAccountingCutoverConflict = errors.New("billing: accounting cutover replay conflict")
	ErrAccountingCutoverNotFound = errors.New("billing: accounting cutover not found")
)

// AccountingCutoverMarker is the durable per-store cutover row.
type AccountingCutoverMarker struct {
	StoreID            string
	Generation         int
	State              AccountingCutoverState
	ActivePostingOwner string
	CompatibilityFloor string
	Version            uint64
	Epoch              uint64
	TransitionID       string
	CreatedAtUnix      int64
	UpdatedAtUnix      int64
}

// AccountingCutoverTransition is the narrow CAS request later 17.3B consumes.
type AccountingCutoverTransition struct {
	ExpectedVersion uint64
	ExpectedEpoch   uint64
	NextState       AccountingCutoverState
	TransitionID    string
}

// Valid reports whether the state is one of the four approved cutover states.
func (s AccountingCutoverState) Valid() bool {
	switch s {
	case AccountingCutoverV1Active, AccountingCutoverV2Shadow, AccountingCutoverV1Draining, AccountingCutoverV2Active:
		return true
	default:
		return false
	}
}

// AccountingCutoverExpectations returns the generation, active posting owner,
// and compatibility floor pinned to one approved state.
func AccountingCutoverExpectations(state AccountingCutoverState) (generation int, owner string, floor string, ok bool) {
	switch state {
	case AccountingCutoverV1Active:
		return AccountingCutoverGenerationV1, AccountingPostingOwnerV1, AccountingCompatFloorV1, true
	case AccountingCutoverV2Shadow:
		return AccountingCutoverGenerationV1, AccountingPostingOwnerV1, AccountingCompatFloorV1, true
	case AccountingCutoverV1Draining:
		return AccountingCutoverGenerationV1, AccountingPostingOwnerV1, AccountingCompatFloorV1, true
	case AccountingCutoverV2Active:
		return AccountingCutoverGenerationV2, AccountingPostingOwnerV2, AccountingCompatFloorV2, true
	default:
		return 0, "", "", false
	}
}

func accountingCutoverAllowedSuccessor(current, next AccountingCutoverState) bool {
	switch current {
	case AccountingCutoverV1Active:
		return next == AccountingCutoverV2Shadow
	case AccountingCutoverV2Shadow:
		return next == AccountingCutoverV1Draining
	case AccountingCutoverV1Draining:
		return next == AccountingCutoverV2Active
	default:
		return false
	}
}

func validateAccountingCutoverStoreID(storeID string) error {
	trimmed := strings.TrimSpace(storeID)
	if trimmed == "" {
		return fmt.Errorf("%w: %w: store scope is required", ErrAccountingCutoverInvalid, ErrInvalidRecord)
	}
	if len(trimmed) > accountingCutoverMaxStoreIDLen {
		return fmt.Errorf("%w: %w: store scope exceeds %d bytes", ErrAccountingCutoverInvalid, ErrInvalidRecord, accountingCutoverMaxStoreIDLen)
	}
	if trimmed != storeID {
		// Surrounding whitespace is never significant; callers must pass the
		// canonical trimmed scope so durable keys are stable.
		return fmt.Errorf("%w: %w: store scope must be trimmed", ErrAccountingCutoverInvalid, ErrInvalidRecord)
	}
	if strings.ContainsAny(storeID, "\x00\r\n") {
		return fmt.Errorf("%w: %w: store scope contains control characters", ErrAccountingCutoverInvalid, ErrInvalidRecord)
	}
	return nil
}

func validateAccountingCutoverTransitionID(transitionID string) error {
	if strings.TrimSpace(transitionID) == "" {
		return fmt.Errorf("%w: %w: transition identity is required", ErrAccountingCutoverInvalid, ErrInvalidRecord)
	}
	if len(transitionID) > accountingCutoverMaxTransitionIDLen {
		return fmt.Errorf("%w: %w: transition identity exceeds %d bytes", ErrAccountingCutoverInvalid, ErrInvalidRecord, accountingCutoverMaxTransitionIDLen)
	}
	if strings.TrimSpace(transitionID) != transitionID {
		return fmt.Errorf("%w: %w: transition identity must be trimmed", ErrAccountingCutoverInvalid, ErrInvalidRecord)
	}
	if strings.ContainsAny(transitionID, "\x00\r\n") {
		return fmt.Errorf("%w: %w: transition identity contains control characters", ErrAccountingCutoverInvalid, ErrInvalidRecord)
	}
	return nil
}

// Validate fails closed on any malformed or inconsistent marker.
func (m AccountingCutoverMarker) Validate() error {
	if err := validateAccountingCutoverStoreID(m.StoreID); err != nil {
		return err
	}
	if !m.State.Valid() {
		return fmt.Errorf("%w: %w: unknown cutover state %q", ErrAccountingCutoverInvalid, ErrInvalidRecord, string(m.State))
	}
	gen, owner, floor, ok := AccountingCutoverExpectations(m.State)
	if !ok {
		return fmt.Errorf("%w: %w: unknown cutover state %q", ErrAccountingCutoverInvalid, ErrInvalidRecord, string(m.State))
	}
	if m.Generation != gen {
		return fmt.Errorf("%w: %w: state %q requires generation %d, got %d", ErrAccountingCutoverInvalid, ErrInvalidRecord, string(m.State), gen, m.Generation)
	}
	if m.ActivePostingOwner != owner {
		return fmt.Errorf("%w: %w: state %q requires posting owner %q, got %q", ErrAccountingCutoverInvalid, ErrInvalidRecord, string(m.State), owner, m.ActivePostingOwner)
	}
	if m.CompatibilityFloor != floor {
		return fmt.Errorf("%w: %w: state %q requires compatibility floor %q, got %q", ErrAccountingCutoverInvalid, ErrInvalidRecord, string(m.State), floor, m.CompatibilityFloor)
	}
	if m.ActivePostingOwner != HistoricalV1WriterVersion && m.ActivePostingOwner != V2WriterVersion {
		return fmt.Errorf("%w: %w: unknown posting owner %q", ErrAccountingCutoverInvalid, ErrInvalidRecord, m.ActivePostingOwner)
	}
	if m.CompatibilityFloor != HistoricalV1WriterVersion && m.CompatibilityFloor != V2WriterVersion {
		return fmt.Errorf("%w: %w: unknown compatibility floor %q", ErrAccountingCutoverInvalid, ErrInvalidRecord, m.CompatibilityFloor)
	}
	if m.Version == 0 || m.Version > math.MaxInt64 {
		return fmt.Errorf("%w: %w: cutover version %d out of range", ErrAccountingCutoverInvalid, ErrInvalidRecord, m.Version)
	}
	if m.Epoch == 0 || m.Epoch > math.MaxInt64 {
		return fmt.Errorf("%w: %w: cutover epoch %d out of range", ErrAccountingCutoverInvalid, ErrInvalidRecord, m.Epoch)
	}
	if err := validateAccountingCutoverTransitionID(m.TransitionID); err != nil {
		return err
	}
	if m.CreatedAtUnix <= 0 || m.UpdatedAtUnix <= 0 {
		return fmt.Errorf("%w: %w: cutover timestamps must be positive", ErrAccountingCutoverInvalid, ErrInvalidRecord)
	}
	if m.UpdatedAtUnix < m.CreatedAtUnix {
		return fmt.Errorf("%w: %w: cutover updated_at precedes created_at", ErrAccountingCutoverInvalid, ErrInvalidRecord)
	}
	return nil
}

// Validate fails closed on malformed CAS requests.
func (t AccountingCutoverTransition) Validate() error {
	if t.ExpectedVersion == 0 || t.ExpectedVersion > math.MaxInt64 {
		return fmt.Errorf("%w: %w: expected version %d out of range", ErrAccountingCutoverInvalid, ErrInvalidRecord, t.ExpectedVersion)
	}
	if t.ExpectedEpoch == 0 || t.ExpectedEpoch > math.MaxInt64 {
		return fmt.Errorf("%w: %w: expected epoch %d out of range", ErrAccountingCutoverInvalid, ErrInvalidRecord, t.ExpectedEpoch)
	}
	if !t.NextState.Valid() {
		return fmt.Errorf("%w: %w: unknown cutover state %q", ErrAccountingCutoverInvalid, ErrInvalidRecord, string(t.NextState))
	}
	if err := validateAccountingCutoverTransitionID(t.TransitionID); err != nil {
		return err
	}
	return nil
}

// DefaultAccountingCutoverMarker returns the safe legacy default for stores
// with no marker: V1 remains the sole posting authority (generation 1,
// version/epoch 1). nowUnix carries the auditable creation timestamp and must
// be positive.
func DefaultAccountingCutoverMarker(storeID string, nowUnix int64) (AccountingCutoverMarker, error) {
	if err := validateAccountingCutoverStoreID(storeID); err != nil {
		return AccountingCutoverMarker{}, err
	}
	if nowUnix <= 0 {
		return AccountingCutoverMarker{}, fmt.Errorf("%w: %w: cutover timestamp out of range", ErrAccountingCutoverInvalid, ErrInvalidRecord)
	}
	marker := AccountingCutoverMarker{
		StoreID:            storeID,
		Generation:         AccountingCutoverGenerationV1,
		State:              AccountingCutoverV1Active,
		ActivePostingOwner: AccountingPostingOwnerV1,
		CompatibilityFloor: AccountingCompatFloorV1,
		Version:            1,
		Epoch:              1,
		TransitionID:       accountingCutoverInitTransitionID,
		CreatedAtUnix:      nowUnix,
		UpdatedAtUnix:      nowUnix,
	}
	if err := marker.Validate(); err != nil {
		return AccountingCutoverMarker{}, err
	}
	return marker, nil
}

// ValidateAccountingCutoverTransition checks one monotonic forward step and
// builds the next marker. Stale expected version/epoch fails with
// ErrAccountingCutoverFence; skipped/backward/same-state/unknown targets fail
// with ErrAccountingCutoverInvalid. nowUnix becomes the next UpdatedAt and
// must be positive.
func ValidateAccountingCutoverTransition(current AccountingCutoverMarker, req AccountingCutoverTransition, nowUnix int64) (AccountingCutoverMarker, error) {
	if err := current.Validate(); err != nil {
		return AccountingCutoverMarker{}, err
	}
	if err := req.Validate(); err != nil {
		return AccountingCutoverMarker{}, err
	}
	if nowUnix <= 0 {
		return AccountingCutoverMarker{}, fmt.Errorf("%w: %w: cutover timestamp out of range", ErrAccountingCutoverInvalid, ErrInvalidRecord)
	}
	if req.ExpectedVersion != current.Version || req.ExpectedEpoch != current.Epoch {
		return AccountingCutoverMarker{}, fmt.Errorf("%w: expected version/epoch %d/%d, current %d/%d",
			ErrAccountingCutoverFence, req.ExpectedVersion, req.ExpectedEpoch, current.Version, current.Epoch)
	}
	if req.NextState == current.State {
		return AccountingCutoverMarker{}, fmt.Errorf("%w: %w: cutover state %q is not forward progress",
			ErrAccountingCutoverInvalid, ErrInvalidRecord, string(req.NextState))
	}
	if !accountingCutoverAllowedSuccessor(current.State, req.NextState) {
		return AccountingCutoverMarker{}, fmt.Errorf("%w: %w: cutover %q -> %q is not an allowed transition",
			ErrAccountingCutoverInvalid, ErrInvalidRecord, string(current.State), string(req.NextState))
	}
	if current.Version >= math.MaxInt64 || current.Epoch >= math.MaxInt64 {
		return AccountingCutoverMarker{}, fmt.Errorf("%w: %w: cutover version/epoch overflow", ErrAccountingCutoverInvalid, ErrInvalidRecord)
	}
	gen, owner, floor, ok := AccountingCutoverExpectations(req.NextState)
	if !ok {
		return AccountingCutoverMarker{}, fmt.Errorf("%w: %w: unknown cutover state %q", ErrAccountingCutoverInvalid, ErrInvalidRecord, string(req.NextState))
	}
	updated := nowUnix
	if updated < current.CreatedAtUnix {
		// Never move the auditable clock backward; preserve CreatedAt ordering.
		updated = current.CreatedAtUnix
	}
	next := AccountingCutoverMarker{
		StoreID:            current.StoreID,
		Generation:         gen,
		State:              req.NextState,
		ActivePostingOwner: owner,
		CompatibilityFloor: floor,
		Version:            current.Version + 1,
		Epoch:              current.Epoch + 1,
		TransitionID:       req.TransitionID,
		CreatedAtUnix:      current.CreatedAtUnix,
		UpdatedAtUnix:      updated,
	}
	if err := next.Validate(); err != nil {
		return AccountingCutoverMarker{}, err
	}
	return next, nil
}

// IsAccountingCutoverReplay reports whether current is the exact durable
// result of req: same expected version/epoch advanced by one, same target
// state, same transition identity. Callers use it to return idempotent
// success on retry; any same-version advance with a different payload is a
// conflict, not a replay.
func IsAccountingCutoverReplay(current AccountingCutoverMarker, req AccountingCutoverTransition) bool {
	return current.Version == req.ExpectedVersion+1 &&
		current.Epoch == req.ExpectedEpoch+1 &&
		current.State == req.NextState &&
		current.TransitionID == req.TransitionID
}
