package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 13.3B durable selected-cost adjustment boundary. The store contract
// consumes one frozen selected valuation plus the caller's compare-and-swap
// read and applies the pure 13.3A planner inside one local transaction. Core
// stays SQL-free: persistence, CAS, immutable adjustment/link rows and balanced
// journal deltas belong to the driven adapter.

var (
	// ErrSelectedCostAdjustmentInvalid identifies malformed selected-cost
	// adjustment input. Invalid input fails closed before a transaction opens.
	ErrSelectedCostAdjustmentInvalid = errors.New("billing: invalid selected cost adjustment")
	// ErrSelectedCostAdjustmentConflict identifies a replay that is not backed
	// by the durable adjustment operation, or a durable head whose identity
	// does not match the caller's CAS read.
	ErrSelectedCostAdjustmentConflict = errors.New("billing: selected cost adjustment conflict")
)

// SelectedCostAdjustmentInput is the durable-store boundary for one selected
// cost head compare-and-swap. Expected is the caller's durable read (version
// plus previously posted selected valuation); Selected is the new frozen
// valuation revision.
type SelectedCostAdjustmentInput struct {
	AccountID string
	CallID    BillingCallID
	HeadKey   string
	Subject   metering.SubjectRef
	Expected  SelectedCostHeadExpectation
	Selected  SelectedCostValuation
}

// Normalize validates and returns a detached copy of the adjustment envelope.
func (in SelectedCostAdjustmentInput) Normalize() (SelectedCostAdjustmentInput, error) {
	out := in
	out.AccountID = strings.TrimSpace(out.AccountID)
	out.HeadKey = strings.TrimSpace(out.HeadKey)
	out.Selected = in.Selected.Clone()
	out.Expected = SelectedCostHeadExpectation{Version: in.Expected.Version}
	if in.Expected.Previous != nil {
		previous := in.Expected.Previous.Clone()
		out.Expected.Previous = &previous
	}
	if !validEconomicIdentity(out.AccountID, metering.MaxSchemaIDBytes) {
		return SelectedCostAdjustmentInput{}, fmt.Errorf("%w: account id is required", ErrSelectedCostAdjustmentInvalid)
	}
	if err := out.CallID.Validate(); err != nil {
		return SelectedCostAdjustmentInput{}, fmt.Errorf("%w: call id: %v", ErrSelectedCostAdjustmentInvalid, err)
	}
	if !validEconomicIdentity(out.HeadKey, metering.MaxSchemaIDBytes) || strings.TrimSpace(out.HeadKey) != out.HeadKey {
		return SelectedCostAdjustmentInput{}, fmt.Errorf("%w: head key is required and must not carry surrounding whitespace", ErrSelectedCostAdjustmentInvalid)
	}
	if err := out.Subject.Validate(); err != nil {
		return SelectedCostAdjustmentInput{}, fmt.Errorf("%w: subject: %v", ErrSelectedCostAdjustmentInvalid, err)
	}
	if out.Subject.Kind != metering.SubjectBLeg && out.Subject.Kind != metering.SubjectProviderCharge {
		return SelectedCostAdjustmentInput{}, fmt.Errorf("%w: request-scoped B-leg/provider-charge subject required", ErrSelectedCostAdjustmentInvalid)
	}
	if out.Subject.AccountID != "" && out.Subject.AccountID != out.AccountID {
		return SelectedCostAdjustmentInput{}, fmt.Errorf("%w: subject account differs from account id", ErrSelectedCostAdjustmentInvalid)
	}
	if out.Subject.BillingCallID != "" && out.Subject.BillingCallID != out.CallID.String() {
		return SelectedCostAdjustmentInput{}, fmt.Errorf("%w: subject billing call differs from call id", ErrSelectedCostAdjustmentInvalid)
	}
	if out.Subject.CallID != "" && out.Subject.CallID != out.CallID.String() {
		return SelectedCostAdjustmentInput{}, fmt.Errorf("%w: subject call differs from call id", ErrSelectedCostAdjustmentInvalid)
	}
	if err := out.Expected.Validate(); err != nil {
		return SelectedCostAdjustmentInput{}, fmt.Errorf("%w: %w", ErrSelectedCostAdjustmentInvalid, err)
	}
	if err := out.Selected.Validate(); err != nil {
		return SelectedCostAdjustmentInput{}, fmt.Errorf("%w: %w", ErrSelectedCostAdjustmentInvalid, err)
	}
	return out, nil
}

// SelectedCostAdjustmentResult is the durable transition outcome. Applied and
// NoOp carry the operation/link identity and the appended journal transaction;
// Replay reproduces the original stable identity from the durable adjustment
// operation. Pending, Stale and Conflict are zero-effect outcomes.
type SelectedCostAdjustmentResult struct {
	AccountID string
	CallID    BillingCallID
	HeadKey   string

	Status     SelectedCostHeadTransitionStatus
	Reason     SelectedCostHeadTransitionReason
	Comparison SelectedCostComparisonStatus
	Posting    SelectedCostPostingStatus

	SelectionStatus OperatorCostSelectionStatus
	SelectionReason OperatorCostSelectionReason

	Previous      *SelectedCostValuationRef
	Current       SelectedCostValuationRef
	Delta         *MonetaryExactAmount
	OperationKey  string
	LinkKey       string
	Fingerprint   string
	HeadVersion   uint64
	TransactionID string
}

// SelectedCostAdjustmentStore owns the only transactional writer for selected-
// cost head adjustments. Implementations must atomically fence the head and
// append the immutable valuation link, balanced journal delta and head
// transition; pure operator COGS never locks or updates a customer balance.
type SelectedCostAdjustmentStore interface {
	ApplySelectedCostAdjustment(context.Context, SelectedCostAdjustmentInput) (SelectedCostAdjustmentResult, error)
}

// SelectedCostHeadReader reads the current selected/posted valuation pointer,
// including the exact frozen valuation identity, dual-dialect adapter identity
// and posting state, without changing any accounting state.
type SelectedCostHeadReader interface {
	GetSelectedCostHead(context.Context, string, BillingCallID, string) (SelectedCostHead, error)
}
