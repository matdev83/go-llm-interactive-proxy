package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Customer-unit errors are intentionally separate from monetary-account and
// supplier-gauge errors. A customer unit balance is a customer-owned
// non-monetary authority; provider account/window observations must never be
// accepted as a source for one of these operations.
var (
	ErrCustomerUnitInvalid = errors.New("billing: invalid customer unit")
	// The more specific names are aliases so callers can classify a failure at
	// either the domain or operation boundary without introducing duplicate
	// error identities.
	ErrCustomerUnitKeyInvalid       = ErrCustomerUnitInvalid
	ErrCustomerUnitBalanceInvalid   = ErrCustomerUnitInvalid
	ErrCustomerUnitOperationInvalid = ErrCustomerUnitInvalid
	ErrCustomerUnitResultInvalid    = ErrCustomerUnitInvalid

	ErrCustomerUnitAuthority           = errors.New("billing: customer unit authority rejected")
	ErrCustomerUnitEntitlementMissing  = errors.New("billing: customer unit entitlement is missing")
	ErrCustomerUnitEntitlementPartial  = errors.New("billing: customer unit entitlement is partial")
	ErrCustomerUnitEntitlementConflict = errors.New("billing: customer unit entitlement is conflicting")
	ErrCustomerUnitInsufficient        = errors.New("billing: insufficient customer units")
	ErrCustomerUnitStaleVersion        = errors.New("billing: stale customer unit version")
	ErrCustomerUnitStaleFence          = errors.New("billing: stale customer unit fence")
	ErrCustomerUnitOperationConflict   = errors.New("billing: customer unit operation replay conflict")
	ErrCustomerUnitReservationInvalid  = errors.New("billing: invalid customer unit reservation")
	ErrCustomerUnitUnavailable         = errors.New("billing: customer unit ledger unavailable")
)

const (
	// CustomerUnitOperationVersionV1 is the first persisted operation contract.
	CustomerUnitOperationVersionV1 uint32 = 1

	// Customer-unit operation identities and account/pool/period scope labels
	// use the same bounded identity size as metering schema identifiers.
	maxCustomerUnitIdentityBytes = metering.MaxSchemaIDBytes
)

// CustomerEntitlementStatus describes the completeness of an authoritative
// customer-owned allowance row. Partial and missing are not zero balances:
// callers must not debit either state.
type CustomerEntitlementStatus string

const (
	CustomerEntitlementComplete CustomerEntitlementStatus = "complete"
	CustomerEntitlementPartial  CustomerEntitlementStatus = "partial"
	CustomerEntitlementMissing  CustomerEntitlementStatus = "missing"
	CustomerEntitlementConflict CustomerEntitlementStatus = "conflict"
)

func (s CustomerEntitlementStatus) known() bool {
	switch s {
	case CustomerEntitlementComplete, CustomerEntitlementPartial,
		CustomerEntitlementMissing, CustomerEntitlementConflict:
		return true
	default:
		return false
	}
}

// CustomerUnitKey is the complete identity of one customer-owned unit pool.
// AccountID, PoolID and PeriodID are deliberately customer scope fields. No
// provider account, provider window, or utilization gauge is part of this key.
// Component carries the unit/component and any approved dimensions.
type CustomerUnitKey struct {
	AccountID string                `json:"account_id"`
	PoolID    string                `json:"pool_id"`
	PeriodID  string                `json:"period_id"`
	Component metering.ComponentKey `json:"component"`
}

// Validate checks trusted customer scope and canonical component identity.
func (k CustomerUnitKey) Validate() error {
	_, err := k.normalized()
	return err
}

func (k CustomerUnitKey) normalized() (CustomerUnitKey, error) {
	if err := validateCustomerUnitIdentity("account_id", k.AccountID); err != nil {
		return CustomerUnitKey{}, fmt.Errorf("%w: %v", ErrCustomerUnitKeyInvalid, err)
	}
	if err := validateCustomerUnitIdentity("pool_id", k.PoolID); err != nil {
		return CustomerUnitKey{}, fmt.Errorf("%w: %v", ErrCustomerUnitKeyInvalid, err)
	}
	if err := validateCustomerUnitIdentity("period_id", k.PeriodID); err != nil {
		return CustomerUnitKey{}, fmt.Errorf("%w: %v", ErrCustomerUnitKeyInvalid, err)
	}
	component, err := k.Component.Normalize()
	if err != nil {
		return CustomerUnitKey{}, fmt.Errorf("%w: component: %v", ErrCustomerUnitKeyInvalid, err)
	}
	k.Component = component
	return k, nil
}

// CanonicalKey returns the complete deterministic customer-unit identity. It
// is suitable for an adapter's canonical-key column and must be compared in
// addition to (rather than replaced by) any hash index.
func (k CustomerUnitKey) CanonicalKey() (string, error) {
	normalized, err := k.normalized()
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		Version   uint32                `json:"version"`
		AccountID string                `json:"account_id"`
		PoolID    string                `json:"pool_id"`
		PeriodID  string                `json:"period_id"`
		Component metering.ComponentKey `json:"component"`
	}{
		Version: 1, AccountID: normalized.AccountID, PoolID: normalized.PoolID,
		PeriodID: normalized.PeriodID, Component: normalized.Component,
	})
	if err != nil {
		return "", fmt.Errorf("%w: identity encoding: %v", ErrCustomerUnitKeyInvalid, err)
	}
	return string(payload), nil
}

// IdentityKey returns a deterministic, bounded digest suitable for a
// balance/operation uniqueness index. The canonical key remains available via
// CanonicalKey and is the collision-safe comparison identity.
func (k CustomerUnitKey) IdentityKey() (string, error) {
	canonical, err := k.CanonicalKey()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(canonical))
	return "customer-unit:v1:" + hex.EncodeToString(digest[:]), nil
}

// Equal compares normalized customer-unit identities. Invalid keys never
// compare equal.
func (k CustomerUnitKey) Equal(other CustomerUnitKey) bool {
	a, errA := k.normalized()
	b, errB := other.normalized()
	return errA == nil && errB == nil && a.AccountID == b.AccountID &&
		a.PoolID == b.PoolID && a.PeriodID == b.PeriodID && a.Component.Equal(b.Component)
}

// CustomerUnitBalance is the authoritative customer-owned allowance state.
// Complete rows satisfy Granted = Available + Reserved + Consumed. Pointer
// quantities allow an adapter to represent a partial row without turning an
// unknown quantity into zero.
type CustomerUnitBalance struct {
	Key       CustomerUnitKey           `json:"key"`
	Status    CustomerEntitlementStatus `json:"status"`
	Granted   *metering.Decimal         `json:"granted,omitempty"`
	Available *metering.Decimal         `json:"available,omitempty"`
	Reserved  *metering.Decimal         `json:"reserved,omitempty"`
	Consumed  *metering.Decimal         `json:"consumed,omitempty"`
	Version   uint64                    `json:"version"`
	Fence     uint64                    `json:"fence"`
}

// Validate rejects incomplete balances for operational use while preserving
// typed partial/missing states for diagnostics and safe fail-closed decisions.
func (b CustomerUnitBalance) Validate() error {
	if err := b.Key.Validate(); err != nil {
		return fmt.Errorf("%w: key: %v", ErrCustomerUnitBalanceInvalid, err)
	}
	if !b.Status.known() {
		return fmt.Errorf("%w: unknown entitlement status %q", ErrCustomerUnitBalanceInvalid, b.Status)
	}
	switch b.Status {
	case CustomerEntitlementMissing:
		if b.Granted != nil || b.Available != nil || b.Reserved != nil || b.Consumed != nil || b.Version != 0 || b.Fence != 0 {
			return fmt.Errorf("%w: missing entitlement must not contain balance state", ErrCustomerUnitBalanceInvalid)
		}
		return nil
	case CustomerEntitlementPartial, CustomerEntitlementConflict:
		for name, value := range map[string]*metering.Decimal{
			"granted": b.Granted, "available": b.Available,
			"reserved": b.Reserved, "consumed": b.Consumed,
		} {
			if value == nil {
				continue
			}
			if _, err := normalizeCustomerUnitDecimal(name, *value, false); err != nil {
				return fmt.Errorf("%w: %v", ErrCustomerUnitBalanceInvalid, err)
			}
		}
		return nil
	case CustomerEntitlementComplete:
		if b.Granted == nil || b.Available == nil || b.Reserved == nil || b.Consumed == nil {
			return fmt.Errorf("%w: complete entitlement requires all balance quantities", ErrCustomerUnitBalanceInvalid)
		}
		if b.Version == 0 || b.Fence == 0 {
			return fmt.Errorf("%w: complete entitlement requires positive version and fence", ErrCustomerUnitBalanceInvalid)
		}
		granted, err := normalizeCustomerUnitDecimal("granted", *b.Granted, false)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrCustomerUnitBalanceInvalid, err)
		}
		available, err := normalizeCustomerUnitDecimal("available", *b.Available, false)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrCustomerUnitBalanceInvalid, err)
		}
		reserved, err := normalizeCustomerUnitDecimal("reserved", *b.Reserved, false)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrCustomerUnitBalanceInvalid, err)
		}
		consumed, err := normalizeCustomerUnitDecimal("consumed", *b.Consumed, false)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrCustomerUnitBalanceInvalid, err)
		}
		sum, err := decimalAdd(available, reserved)
		if err != nil {
			return fmt.Errorf("%w: balance sum: %v", ErrCustomerUnitBalanceInvalid, err)
		}
		sum, err = decimalAdd(sum, consumed)
		if err != nil {
			return fmt.Errorf("%w: balance sum: %v", ErrCustomerUnitBalanceInvalid, err)
		}
		if !sum.Equal(granted) {
			return fmt.Errorf("%w: granted must equal available plus reserved plus consumed", ErrCustomerUnitBalanceInvalid)
		}
	}
	return nil
}

// Clone deep-copies decimal pointers and component dimensions.
func (b CustomerUnitBalance) Clone() CustomerUnitBalance {
	b.Key.Component = b.Key.Component.Clone()
	b.Granted = cloneCustomerDecimal(b.Granted)
	b.Available = cloneCustomerDecimal(b.Available)
	b.Reserved = cloneCustomerDecimal(b.Reserved)
	b.Consumed = cloneCustomerDecimal(b.Consumed)
	return b
}

// CustomerUnitOperationKind is one atomic mutation of a customer unit row.
type CustomerUnitOperationKind string

const (
	CustomerUnitOperationGrant   CustomerUnitOperationKind = "grant"
	CustomerUnitOperationDebit   CustomerUnitOperationKind = "debit"
	CustomerUnitOperationReserve CustomerUnitOperationKind = "reserve"
	CustomerUnitOperationCommit  CustomerUnitOperationKind = "commit"
	CustomerUnitOperationRelease CustomerUnitOperationKind = "release"
)

func (k CustomerUnitOperationKind) known() bool {
	switch k {
	case CustomerUnitOperationGrant, CustomerUnitOperationDebit,
		CustomerUnitOperationReserve, CustomerUnitOperationCommit,
		CustomerUnitOperationRelease:
		return true
	default:
		return false
	}
}

// CustomerUnitOperationSource identifies a customer-owned authority. The
// provider-account/window source is intentionally absent; unknown values are
// rejected rather than interpreted as customer credit.
type CustomerUnitOperationSource string

const (
	CustomerUnitOperationSourceCustomerProvisioning CustomerUnitOperationSource = "customer_provisioning"
	CustomerUnitOperationSourceRetailSettlement     CustomerUnitOperationSource = "retail_settlement"
	// CustomerUnitOperationSourceCustomerSettlement is a descriptive alias for
	// callers that use the settlement terminology from the posting boundary.
	CustomerUnitOperationSourceCustomerSettlement CustomerUnitOperationSource = CustomerUnitOperationSourceRetailSettlement
	CustomerUnitOperationSourceCustomerPolicy     CustomerUnitOperationSource = "customer_policy"
)

func (s CustomerUnitOperationSource) customerOwned() bool {
	switch s {
	case CustomerUnitOperationSourceCustomerProvisioning,
		CustomerUnitOperationSourceRetailSettlement,
		CustomerUnitOperationSourceCustomerPolicy:
		return true
	default:
		return false
	}
}

// CustomerUnitOperation is the idempotent command consumed by the unit
// ledger. ExpectedVersion and Fence are checked by the same atomic operation
// that mutates the row; they are not an invitation to perform a read-then-write
// sequence in an adapter.
type CustomerUnitOperation struct {
	Version               uint32                      `json:"version"`
	OperationID           string                      `json:"operation_id"`
	Key                   CustomerUnitKey             `json:"key"`
	Kind                  CustomerUnitOperationKind   `json:"kind"`
	Source                CustomerUnitOperationSource `json:"source"`
	Quantity              metering.Decimal            `json:"quantity"`
	ReservationID         string                      `json:"reservation_id,omitempty"`
	ExpectedVersion       uint64                      `json:"expected_version"`
	Fence                 uint64                      `json:"fence"`
	MonetaryFallbackBound *Money                      `json:"monetary_fallback_bound,omitempty"`
}

// Validate checks operation identity, source ownership, quantities and
// compare-and-fence preconditions.
func (op CustomerUnitOperation) Validate() error {
	if op.Version != CustomerUnitOperationVersionV1 {
		return fmt.Errorf("%w: unsupported operation version %d", ErrCustomerUnitOperationInvalid, op.Version)
	}
	if err := validateCustomerUnitIdentity("operation_id", op.OperationID); err != nil {
		return fmt.Errorf("%w: %v", ErrCustomerUnitOperationInvalid, err)
	}
	if err := op.Key.Validate(); err != nil {
		return fmt.Errorf("%w: key: %v", ErrCustomerUnitOperationInvalid, err)
	}
	if !op.Kind.known() {
		return fmt.Errorf("%w: unknown operation kind %q", ErrCustomerUnitOperationInvalid, op.Kind)
	}
	if !op.Source.customerOwned() {
		return fmt.Errorf("%w: source %q is not customer-owned", ErrCustomerUnitAuthority, op.Source)
	}
	if _, err := normalizeCustomerUnitDecimal("quantity", op.Quantity, true); err != nil {
		return fmt.Errorf("%w: %v", ErrCustomerUnitOperationInvalid, err)
	}
	if op.Fence == 0 {
		return fmt.Errorf("%w: positive fencing token required", ErrCustomerUnitStaleFence)
	}
	if op.Kind == CustomerUnitOperationGrant {
		if op.ReservationID != "" {
			return fmt.Errorf("%w: grant must not carry reservation id", ErrCustomerUnitReservationInvalid)
		}
	} else {
		if op.ExpectedVersion == 0 {
			return fmt.Errorf("%w: positive expected version required", ErrCustomerUnitStaleVersion)
		}
		switch op.Kind {
		case CustomerUnitOperationReserve, CustomerUnitOperationCommit, CustomerUnitOperationRelease:
			if err := validateCustomerUnitIdentity("reservation_id", op.ReservationID); err != nil {
				return fmt.Errorf("%w: %v", ErrCustomerUnitReservationInvalid, err)
			}
		case CustomerUnitOperationDebit:
			if op.ReservationID != "" {
				return fmt.Errorf("%w: direct debit must not carry reservation id", ErrCustomerUnitReservationInvalid)
			}
		}
	}
	if op.MonetaryFallbackBound != nil {
		if op.Kind != CustomerUnitOperationDebit && op.Kind != CustomerUnitOperationReserve {
			return fmt.Errorf("%w: monetary fallback is only valid for debit or reserve", ErrCustomerUnitOperationInvalid)
		}
		if err := validateCustomerMonetaryBound(*op.MonetaryFallbackBound); err != nil {
			return err
		}
	}
	return nil
}

// SemanticFingerprint identifies the operation payload. Compare-and-fence
// values are excluded so an identical retried operation can replay after the
// first transaction has advanced the balance version.
func (op CustomerUnitOperation) SemanticFingerprint() (string, error) {
	if err := op.Validate(); err != nil {
		return "", err
	}
	normalizedKey, err := op.Key.normalized()
	if err != nil {
		return "", err
	}
	quantity, err := op.Quantity.Normalize()
	if err != nil {
		return "", err
	}
	var fallback *Money
	if op.MonetaryFallbackBound != nil {
		value := *op.MonetaryFallbackBound
		fallback = &value
	}
	payload, err := json.Marshal(struct {
		Version       uint32                      `json:"version"`
		OperationID   string                      `json:"operation_id"`
		Key           CustomerUnitKey             `json:"key"`
		Kind          CustomerUnitOperationKind   `json:"kind"`
		Source        CustomerUnitOperationSource `json:"source"`
		Quantity      metering.Decimal            `json:"quantity"`
		ReservationID string                      `json:"reservation_id,omitempty"`
		Fallback      *Money                      `json:"monetary_fallback_bound,omitempty"`
	}{
		Version: CustomerUnitOperationVersionV1, OperationID: op.OperationID,
		Key: normalizedKey, Kind: op.Kind, Source: op.Source,
		Quantity: quantity, ReservationID: op.ReservationID, Fallback: fallback,
	})
	if err != nil {
		return "", fmt.Errorf("%w: fingerprint encoding: %v", ErrCustomerUnitOperationInvalid, err)
	}
	digest := sha256.Sum256(payload)
	return "customer-unit-operation:v1:" + hex.EncodeToString(digest[:]), nil
}

// CheckCustomerUnitOperationReplay accepts an exact payload replay and
// rejects reuse of an operation identity with a different economic meaning.
// ExpectedVersion and Fence are intentionally not part of the replay payload.
func CheckCustomerUnitOperationReplay(existing, incoming CustomerUnitOperation) error {
	if err := existing.Validate(); err != nil {
		return err
	}
	if err := incoming.Validate(); err != nil {
		return err
	}
	if existing.OperationID != incoming.OperationID || !existing.Key.Equal(incoming.Key) {
		return ErrCustomerUnitOperationConflict
	}
	existingFingerprint, err := existing.SemanticFingerprint()
	if err != nil {
		return err
	}
	incomingFingerprint, err := incoming.SemanticFingerprint()
	if err != nil {
		return err
	}
	if existingFingerprint != incomingFingerprint {
		return ErrCustomerUnitOperationConflict
	}
	return nil
}

// CheckCustomerUnitPrecondition enforces the optimistic version and monotonic
// fencing contract against the exact balance locked by a ledger adapter.
func CheckCustomerUnitPrecondition(op CustomerUnitOperation, balance CustomerUnitBalance) error {
	if err := op.Validate(); err != nil {
		return err
	}
	if err := balance.Validate(); err != nil {
		return err
	}
	if !op.Key.Equal(balance.Key) {
		return fmt.Errorf("%w: operation and balance key differ", ErrCustomerUnitInvalid)
	}
	if op.Fence < balance.Fence {
		return fmt.Errorf("%w: operation fence %d is behind balance fence %d", ErrCustomerUnitStaleFence, op.Fence, balance.Fence)
	}
	if balance.Status == CustomerEntitlementMissing && op.Kind == CustomerUnitOperationGrant && op.ExpectedVersion == 0 {
		return nil
	}
	if op.ExpectedVersion == 0 || op.ExpectedVersion != balance.Version {
		return fmt.Errorf("%w: operation version %d does not match balance version %d", ErrCustomerUnitStaleVersion, op.ExpectedVersion, balance.Version)
	}
	return nil
}

// CustomerUnitOperationStatus tells an adapter whether it applied or replayed
// an idempotent command.
type CustomerUnitOperationStatus string

const (
	CustomerUnitOperationApplied  CustomerUnitOperationStatus = "applied"
	CustomerUnitOperationReplayed CustomerUnitOperationStatus = "replayed"
)

// CustomerUnitOperationResult is the immutable result of one atomic ledger
// call. FallbackRequired means only the uncovered quantity is eligible for a
// separately rated monetary charge bounded by FallbackBound.
type CustomerUnitOperationResult struct {
	OperationID       string                      `json:"operation_id"`
	Key               CustomerUnitKey             `json:"key"`
	Kind              CustomerUnitOperationKind   `json:"kind"`
	Status            CustomerUnitOperationStatus `json:"status"`
	Replayed          bool                        `json:"replayed"`
	Entitlement       CustomerEntitlementStatus   `json:"entitlement"`
	Before            CustomerUnitBalance         `json:"before"`
	After             CustomerUnitBalance         `json:"after"`
	AppliedQuantity   *metering.Decimal           `json:"applied_quantity,omitempty"`
	UncoveredQuantity *metering.Decimal           `json:"uncovered_quantity,omitempty"`
	FallbackRequired  bool                        `json:"fallback_required"`
	FallbackBound     *Money                      `json:"fallback_bound,omitempty"`
	Fingerprint       string                      `json:"fingerprint"`
	ReservationID     string                      `json:"reservation_id,omitempty"`
}

func (s CustomerUnitOperationStatus) known() bool {
	return s == CustomerUnitOperationApplied || s == CustomerUnitOperationReplayed
}

// ValidateFor proves that a result belongs to the operation that requested it
// and that no invalid/negative balance or fallback quantity crossed the port.
func (r CustomerUnitOperationResult) ValidateFor(op CustomerUnitOperation) error {
	if err := op.Validate(); err != nil {
		return err
	}
	if !r.Status.known() || r.Replayed != (r.Status == CustomerUnitOperationReplayed) {
		return fmt.Errorf("%w: invalid result status/replay flag", ErrCustomerUnitResultInvalid)
	}
	if r.OperationID != op.OperationID || r.Kind != op.Kind || !r.Key.Equal(op.Key) {
		return fmt.Errorf("%w: result operation identity mismatch", ErrCustomerUnitResultInvalid)
	}
	if r.ReservationID != op.ReservationID {
		return fmt.Errorf("%w: result reservation identity mismatch", ErrCustomerUnitResultInvalid)
	}
	if err := r.Before.Validate(); err != nil {
		return fmt.Errorf("%w: before: %v", ErrCustomerUnitResultInvalid, err)
	}
	if err := r.After.Validate(); err != nil {
		return fmt.Errorf("%w: after: %v", ErrCustomerUnitResultInvalid, err)
	}
	if !r.Before.Key.Equal(op.Key) || !r.After.Key.Equal(op.Key) {
		return fmt.Errorf("%w: result balance key mismatch", ErrCustomerUnitResultInvalid)
	}
	if r.Entitlement != r.After.Status {
		return fmt.Errorf("%w: result entitlement does not match after state", ErrCustomerUnitResultInvalid)
	}
	fingerprint, err := op.SemanticFingerprint()
	if err != nil {
		return err
	}
	if r.Fingerprint != fingerprint {
		return fmt.Errorf("%w: result fingerprint mismatch", ErrCustomerUnitResultInvalid)
	}
	if r.AppliedQuantity == nil || r.UncoveredQuantity == nil {
		return fmt.Errorf("%w: applied and uncovered quantities are required", ErrCustomerUnitResultInvalid)
	}
	applied, err := normalizeCustomerUnitDecimal("applied_quantity", *r.AppliedQuantity, false)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCustomerUnitResultInvalid, err)
	}
	uncovered, err := normalizeCustomerUnitDecimal("uncovered_quantity", *r.UncoveredQuantity, false)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCustomerUnitResultInvalid, err)
	}
	total, err := decimalAdd(applied, uncovered)
	if err != nil {
		return fmt.Errorf("%w: quantity sum: %v", ErrCustomerUnitResultInvalid, err)
	}
	quantity, err := op.Quantity.Normalize()
	if err != nil {
		return err
	}
	if !total.Equal(quantity) {
		return fmt.Errorf("%w: applied plus uncovered does not equal operation quantity", ErrCustomerUnitResultInvalid)
	}
	if r.FallbackRequired {
		if r.FallbackBound == nil || uncovered.Coefficient == "0" {
			return fmt.Errorf("%w: fallback requires a positive uncovered quantity and bound", ErrCustomerUnitResultInvalid)
		}
		if op.MonetaryFallbackBound == nil || *r.FallbackBound != *op.MonetaryFallbackBound {
			return fmt.Errorf("%w: fallback bound does not match operation", ErrCustomerUnitResultInvalid)
		}
		if err := validateCustomerMonetaryBound(*r.FallbackBound); err != nil {
			return fmt.Errorf("%w: fallback bound: %v", ErrCustomerUnitResultInvalid, err)
		}
	} else {
		if r.FallbackBound != nil || uncovered.Coefficient != "0" {
			return fmt.Errorf("%w: uncovered quantity requires a bounded fallback", ErrCustomerUnitResultInvalid)
		}
	}
	if r.After.Version < r.Before.Version || r.After.Fence < r.Before.Fence {
		return fmt.Errorf("%w: balance version/fence moved backwards", ErrCustomerUnitResultInvalid)
	}
	return nil
}

// CustomerUnitLedger is the sole consumed customer-unit mutation port. An
// implementation must perform idempotency lookup, row locking, version/fence
// comparison, transition, reservation binding, and any customer posting in a
// single authoritative transaction. It intentionally has no GetBalance method:
// callers cannot accidentally assemble a stale read-then-spend sequence.
type CustomerUnitLedger interface {
	ApplyCustomerUnitOperation(context.Context, CustomerUnitOperation) (CustomerUnitOperationResult, error)
}

// CustomerUnitOperationStore is a descriptive alias used by adapters that
// name their durable implementation a store.
type CustomerUnitOperationStore = CustomerUnitLedger

// ApplyCustomerUnitOperation validates and invokes exactly one atomic ledger
// operation. It validates the returned result before exposing it to callers.
func ApplyCustomerUnitOperation(ctx context.Context, ledger CustomerUnitLedger, op CustomerUnitOperation) (CustomerUnitOperationResult, error) {
	if ctx == nil {
		return CustomerUnitOperationResult{}, fmt.Errorf("%w: nil context", ErrCustomerUnitInvalid)
	}
	if err := ctx.Err(); err != nil {
		return CustomerUnitOperationResult{}, fmt.Errorf("%w: context: %v", ErrCustomerUnitUnavailable, err)
	}
	if ledger == nil {
		return CustomerUnitOperationResult{}, fmt.Errorf("%w: ledger is required", ErrCustomerUnitUnavailable)
	}
	if err := op.Validate(); err != nil {
		return CustomerUnitOperationResult{}, err
	}
	result, err := ledger.ApplyCustomerUnitOperation(ctx, op)
	if err != nil {
		return CustomerUnitOperationResult{}, err
	}
	if err := result.ValidateFor(op); err != nil {
		return CustomerUnitOperationResult{}, err
	}
	return result, nil
}

// IncludedAllowanceInput asks how much of a requested customer quantity can be
// covered by one complete customer-owned entitlement. MonetaryFallbackBound
// is a maximum monetary exposure for only the uncovered quantity; this domain
// does not convert units to money.
type IncludedAllowanceInput struct {
	Key                   CustomerUnitKey
	Balance               CustomerUnitBalance
	Requested             metering.Decimal
	MonetaryFallbackBound *Money
}

// IncludedAllowanceDecision preserves exact included and uncovered quantities.
// A non-nil bound is carried only when a bounded monetary fallback is needed.
type IncludedAllowanceDecision struct {
	Key                   CustomerUnitKey
	Entitlement           CustomerEntitlementStatus
	Requested             metering.Decimal
	Available             metering.Decimal
	Included              metering.Decimal
	Uncovered             metering.Decimal
	FallbackRequired      bool
	MonetaryFallbackBound *Money
}

// Validate checks the internal exact-quantity and fallback invariants.
func (d IncludedAllowanceDecision) Validate() error {
	if err := d.Key.Validate(); err != nil {
		return fmt.Errorf("%w: decision key: %v", ErrCustomerUnitInvalid, err)
	}
	if !d.Entitlement.known() {
		return fmt.Errorf("%w: unknown decision entitlement %q", ErrCustomerUnitInvalid, d.Entitlement)
	}
	requested, err := normalizeCustomerUnitDecimal("requested", d.Requested, false)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCustomerUnitInvalid, err)
	}
	available, err := normalizeCustomerUnitDecimal("available", d.Available, false)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCustomerUnitInvalid, err)
	}
	included, err := normalizeCustomerUnitDecimal("included", d.Included, false)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCustomerUnitInvalid, err)
	}
	uncovered, err := normalizeCustomerUnitDecimal("uncovered", d.Uncovered, false)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCustomerUnitInvalid, err)
	}
	if cmp, _ := decimalCompare(included, requested); cmp > 0 {
		return fmt.Errorf("%w: included quantity exceeds request", ErrCustomerUnitInvalid)
	}
	if cmp, _ := decimalCompare(included, available); cmp > 0 {
		return fmt.Errorf("%w: included quantity exceeds available quantity", ErrCustomerUnitInvalid)
	}
	total, err := decimalAdd(included, uncovered)
	if err != nil || !total.Equal(requested) {
		return fmt.Errorf("%w: included plus uncovered must equal request", ErrCustomerUnitInvalid)
	}
	if d.FallbackRequired {
		if d.MonetaryFallbackBound == nil || uncovered.Coefficient == "0" {
			return fmt.Errorf("%w: fallback requires uncovered quantity and a bound", ErrCustomerUnitInvalid)
		}
		if err := validateCustomerMonetaryBound(*d.MonetaryFallbackBound); err != nil {
			return err
		}
	} else if d.MonetaryFallbackBound != nil {
		return fmt.Errorf("%w: fallback bound supplied without fallback", ErrCustomerUnitInvalid)
	}
	return nil
}

// EvaluateIncludedAllowance performs a pure exact evaluation. It never writes
// the balance; the caller must submit a debit/reservation operation through
// CustomerUnitLedger for the final atomic decision.
func EvaluateIncludedAllowance(in IncludedAllowanceInput) (IncludedAllowanceDecision, error) {
	normalizedKey, err := in.Key.normalized()
	if err != nil {
		return IncludedAllowanceDecision{}, err
	}
	if err := in.Balance.Validate(); err != nil {
		return IncludedAllowanceDecision{}, err
	}
	if !normalizedKey.Equal(in.Balance.Key) {
		return IncludedAllowanceDecision{}, fmt.Errorf("%w: input and balance key differ", ErrCustomerUnitInvalid)
	}
	requested, err := normalizeCustomerUnitDecimal("requested", in.Requested, true)
	if err != nil {
		return IncludedAllowanceDecision{}, err
	}
	if in.MonetaryFallbackBound != nil {
		if err := validateCustomerMonetaryBound(*in.MonetaryFallbackBound); err != nil {
			return IncludedAllowanceDecision{}, err
		}
	}
	zero := customerUnitZero()
	decision := IncludedAllowanceDecision{
		Key: normalizedKey, Entitlement: in.Balance.Status, Requested: requested,
		Available: zero, Included: zero, Uncovered: requested,
	}
	switch in.Balance.Status {
	case CustomerEntitlementMissing:
		return decision, ErrCustomerUnitEntitlementMissing
	case CustomerEntitlementPartial:
		return decision, ErrCustomerUnitEntitlementPartial
	case CustomerEntitlementConflict:
		return decision, ErrCustomerUnitEntitlementConflict
	case CustomerEntitlementComplete:
		available, err := normalizeCustomerUnitDecimal("available", *in.Balance.Available, false)
		if err != nil {
			return decision, err
		}
		decision.Available = available
		cmp, err := decimalCompare(requested, available)
		if err != nil {
			return decision, err
		}
		if cmp <= 0 {
			decision.Included = requested
			decision.Uncovered = zero
			return decision, nil
		}
		decision.Included = available
		decision.Uncovered, err = decimalSub(requested, available)
		if err != nil {
			return decision, err
		}
		if in.MonetaryFallbackBound == nil {
			return decision, ErrCustomerUnitInsufficient
		}
		bound := *in.MonetaryFallbackBound
		decision.FallbackRequired = true
		decision.MonetaryFallbackBound = &bound
		return decision, nil
	default:
		return decision, fmt.Errorf("%w: unknown entitlement status %q", ErrCustomerUnitInvalid, in.Balance.Status)
	}
}

// ValidateCustomerMonetaryFallback proves a concrete monetary charge is a
// non-negative same-currency amount within a previously admitted bound. It is
// intended for the settlement adapter after exact uncovered units are rated.
func ValidateCustomerMonetaryFallback(amount, bound Money) error {
	if err := validateCustomerMonetaryBound(bound); err != nil {
		return err
	}
	if err := amount.Validate(); err != nil {
		return err
	}
	if amount.Nano < 0 {
		return fmt.Errorf("%w: fallback amount cannot be negative", ErrCustomerUnitInvalid)
	}
	if amount.Currency != bound.Currency {
		return ErrMoneyCurrencyMismatch
	}
	if amount.Nano > bound.Nano {
		return fmt.Errorf("%w: fallback amount %d exceeds bound %d", ErrCustomerUnitInvalid, amount.Nano, bound.Nano)
	}
	return nil
}

// CustomerUnitTransition is the pure state transition an atomic adapter runs
// while holding its balance/reservation transaction. Decision contains the
// included/fallback split for debit and reserve operations.
type CustomerUnitTransition struct {
	Before          CustomerUnitBalance
	After           CustomerUnitBalance
	Decision        IncludedAllowanceDecision
	AppliedQuantity metering.Decimal
}

// TransitionCustomerUnitBalance applies one validated operation to one locked
// balance. It does no I/O, and on failure returns the original state in After.
func TransitionCustomerUnitBalance(op CustomerUnitOperation, before CustomerUnitBalance) (CustomerUnitTransition, error) {
	if err := op.Validate(); err != nil {
		return CustomerUnitTransition{}, err
	}
	if err := before.Validate(); err != nil {
		return CustomerUnitTransition{}, err
	}
	if !op.Key.Equal(before.Key) {
		return CustomerUnitTransition{}, fmt.Errorf("%w: operation and balance key differ", ErrCustomerUnitInvalid)
	}
	zero := customerUnitZero()
	initialDecision := IncludedAllowanceDecision{
		Key: op.Key, Entitlement: before.Status, Requested: op.Quantity,
		Available: zero, Included: zero, Uncovered: zero,
	}
	if before.Status == CustomerEntitlementMissing {
		if op.Kind != CustomerUnitOperationGrant {
			initialDecision.Uncovered = op.Quantity
			return CustomerUnitTransition{Before: before.Clone(), After: before.Clone(), Decision: initialDecision}, ErrCustomerUnitEntitlementMissing
		}
		if err := CheckCustomerUnitPrecondition(op, before); err != nil {
			return CustomerUnitTransition{Before: before.Clone(), After: before.Clone(), Decision: initialDecision}, err
		}
		quantity, _ := op.Quantity.Normalize()
		after := CustomerUnitBalance{
			Key: op.Key, Status: CustomerEntitlementComplete,
			Granted: &quantity, Available: cloneCustomerDecimal(&quantity),
			Reserved: cloneCustomerDecimal(&zero), Consumed: cloneCustomerDecimal(&zero),
			Version: 1, Fence: op.Fence,
		}
		if err := after.Validate(); err != nil {
			return CustomerUnitTransition{}, err
		}
		initialDecision.Entitlement = CustomerEntitlementComplete
		initialDecision.Available = quantity
		initialDecision.Included = quantity
		return CustomerUnitTransition{Before: before.Clone(), After: after, Decision: initialDecision, AppliedQuantity: quantity}, nil
	}
	if before.Status == CustomerEntitlementPartial {
		return CustomerUnitTransition{Before: before.Clone(), After: before.Clone(), Decision: initialDecision}, ErrCustomerUnitEntitlementPartial
	}
	if before.Status == CustomerEntitlementConflict {
		return CustomerUnitTransition{Before: before.Clone(), After: before.Clone(), Decision: initialDecision}, ErrCustomerUnitEntitlementConflict
	}
	if err := CheckCustomerUnitPrecondition(op, before); err != nil {
		return CustomerUnitTransition{Before: before.Clone(), After: before.Clone(), Decision: initialDecision}, err
	}

	current := before.Clone()
	current.Status = CustomerEntitlementComplete
	decision := initialDecision
	decision.Available = *current.Available
	var applied metering.Decimal
	var err error
	switch op.Kind {
	case CustomerUnitOperationGrant:
		quantity, _ := op.Quantity.Normalize()
		current.Granted, err = decimalPointerAdd(current.Granted, quantity)
		if err != nil {
			return CustomerUnitTransition{}, err
		}
		current.Available, err = decimalPointerAdd(current.Available, quantity)
		if err != nil {
			return CustomerUnitTransition{}, err
		}
		applied = quantity
		decision.Available = quantity
		decision.Included = quantity
	case CustomerUnitOperationDebit, CustomerUnitOperationReserve:
		decision, err = EvaluateIncludedAllowance(IncludedAllowanceInput{
			Key: op.Key, Balance: before, Requested: op.Quantity,
			MonetaryFallbackBound: op.MonetaryFallbackBound,
		})
		if err != nil {
			return CustomerUnitTransition{Before: before.Clone(), After: before.Clone(), Decision: decision}, err
		}
		applied = decision.Included
		current.Available, err = decimalPointerSub(current.Available, applied)
		if err != nil {
			return CustomerUnitTransition{}, err
		}
		if op.Kind == CustomerUnitOperationDebit {
			current.Consumed, err = decimalPointerAdd(current.Consumed, applied)
		} else {
			current.Reserved, err = decimalPointerAdd(current.Reserved, applied)
		}
		if err != nil {
			return CustomerUnitTransition{}, err
		}
	case CustomerUnitOperationCommit:
		quantity, _ := op.Quantity.Normalize()
		decision.Available = *current.Reserved
		if cmp, compareErr := decimalCompare(quantity, *current.Reserved); compareErr != nil {
			return CustomerUnitTransition{}, compareErr
		} else if cmp > 0 {
			return CustomerUnitTransition{Before: before.Clone(), After: before.Clone(), Decision: decision}, ErrCustomerUnitInsufficient
		}
		current.Reserved, err = decimalPointerSub(current.Reserved, quantity)
		if err == nil {
			current.Consumed, err = decimalPointerAdd(current.Consumed, quantity)
		}
		applied = quantity
		decision.Included = quantity
	case CustomerUnitOperationRelease:
		quantity, _ := op.Quantity.Normalize()
		decision.Available = *current.Reserved
		if cmp, compareErr := decimalCompare(quantity, *current.Reserved); compareErr != nil {
			return CustomerUnitTransition{}, compareErr
		} else if cmp > 0 {
			return CustomerUnitTransition{Before: before.Clone(), After: before.Clone(), Decision: decision}, ErrCustomerUnitInsufficient
		}
		current.Reserved, err = decimalPointerSub(current.Reserved, quantity)
		if err == nil {
			current.Available, err = decimalPointerAdd(current.Available, quantity)
		}
		applied = quantity
		decision.Included = quantity
	}
	if err != nil {
		return CustomerUnitTransition{}, err
	}
	if applied.Coefficient != "0" {
		if current.Version == math.MaxUint64 {
			return CustomerUnitTransition{}, fmt.Errorf("%w: version overflow", ErrCustomerUnitInvalid)
		}
		current.Version++
	}
	if current.Fence < op.Fence {
		current.Fence = op.Fence
	}
	if err := current.Validate(); err != nil {
		return CustomerUnitTransition{}, err
	}
	decision.Entitlement = current.Status
	return CustomerUnitTransition{Before: before.Clone(), After: current, Decision: decision, AppliedQuantity: applied}, nil
}

func validateCustomerUnitIdentity(name, value string) error {
	if value == "" || len(value) > maxCustomerUnitIdentityBytes || !utf8.ValidString(value) {
		return fmt.Errorf("%s is required, valid UTF-8, and at most %d bytes", name, maxCustomerUnitIdentityBytes)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not contain surrounding whitespace", name)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s must not contain control characters", name)
		}
	}
	return nil
}

func normalizeCustomerUnitDecimal(name string, value metering.Decimal, positive bool) (metering.Decimal, error) {
	normalized, err := value.Normalize()
	if err != nil {
		return metering.Decimal{}, fmt.Errorf("%s: %w", name, err)
	}
	rational, err := normalized.ToRat()
	if err != nil {
		return metering.Decimal{}, fmt.Errorf("%s: %w", name, err)
	}
	if rational.Sign() < 0 {
		return metering.Decimal{}, fmt.Errorf("%s cannot be negative", name)
	}
	if positive && rational.Sign() == 0 {
		return metering.Decimal{}, fmt.Errorf("%s must be positive", name)
	}
	return normalized, nil
}

func validateCustomerMonetaryBound(bound Money) error {
	if err := bound.Validate(); err != nil {
		return err
	}
	if bound.Nano < 0 {
		return fmt.Errorf("%w: monetary fallback bound cannot be negative", ErrCustomerUnitInvalid)
	}
	return nil
}

func customerUnitZero() metering.Decimal { return metering.Decimal{Coefficient: "0"} }

func cloneCustomerDecimal(value *metering.Decimal) *metering.Decimal {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func decimalCompare(a, b metering.Decimal) (int, error) {
	ra, err := a.ToRat()
	if err != nil {
		return 0, err
	}
	rb, err := b.ToRat()
	if err != nil {
		return 0, err
	}
	return ra.Cmp(rb), nil
}

func decimalAdd(a, b metering.Decimal) (metering.Decimal, error) {
	return decimalAtCommonScale(a, b, 1)
}

func decimalSub(a, b metering.Decimal) (metering.Decimal, error) {
	return decimalAtCommonScale(a, b, -1)
}

// decimalAtCommonScale computes a +/- b using bounded base-10 coefficients,
// preserving exactness without float conversion or implicit rounding.
func decimalAtCommonScale(a, b metering.Decimal, signB int) (metering.Decimal, error) {
	left, err := a.Normalize()
	if err != nil {
		return metering.Decimal{}, err
	}
	right, err := b.Normalize()
	if err != nil {
		return metering.Decimal{}, err
	}
	scale := left.Scale
	if right.Scale > scale {
		scale = right.Scale
	}
	leftCoefficient, ok := new(big.Int).SetString(left.Coefficient, 10)
	if !ok {
		return metering.Decimal{}, fmt.Errorf("%w: left coefficient", metering.ErrInvalidDecimal)
	}
	rightCoefficient, ok := new(big.Int).SetString(right.Coefficient, 10)
	if !ok {
		return metering.Decimal{}, fmt.Errorf("%w: right coefficient", metering.ErrInvalidDecimal)
	}
	leftCoefficient.Mul(leftCoefficient, decimalPowerOfTen(uint8(scale-left.Scale)))
	rightCoefficient.Mul(rightCoefficient, decimalPowerOfTen(uint8(scale-right.Scale)))
	if signB < 0 {
		rightCoefficient.Neg(rightCoefficient)
	}
	leftCoefficient.Add(leftCoefficient, rightCoefficient)
	return (metering.Decimal{Coefficient: leftCoefficient.String(), Scale: scale}).Normalize()
}

func decimalPowerOfTen(scale uint8) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
}

func decimalPointerAdd(current *metering.Decimal, delta metering.Decimal) (*metering.Decimal, error) {
	if current == nil {
		return nil, fmt.Errorf("%w: missing balance quantity", ErrCustomerUnitBalanceInvalid)
	}
	value, err := decimalAdd(*current, delta)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func decimalPointerSub(current *metering.Decimal, delta metering.Decimal) (*metering.Decimal, error) {
	if current == nil {
		return nil, fmt.Errorf("%w: missing balance quantity", ErrCustomerUnitBalanceInvalid)
	}
	value, err := decimalSub(*current, delta)
	if err != nil {
		return nil, err
	}
	if _, err := normalizeCustomerUnitDecimal("balance quantity", value, false); err != nil {
		return nil, err
	}
	return &value, nil
}
