package billing

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrProcessingInvalid       = errors.New("billing: invalid usage-record processing state")
	ErrProcessingConflict      = errors.New("billing: usage-record processing fingerprint conflict")
	ErrProcessingLeaseConflict = errors.New("billing: usage-record processing lease ownership conflict")
)

type ProcessingStatus string

const (
	ProcessingPending          ProcessingStatus = "pending"
	ProcessingProcessing       ProcessingStatus = "processing"
	ProcessingRetryable        ProcessingStatus = "retryable"
	ProcessingProcessed        ProcessingStatus = "processed"
	ProcessingUnreconciledCost ProcessingStatus = "unreconciled_cost"
	ProcessingTerminalError    ProcessingStatus = "terminal_error"
)

func (s ProcessingStatus) Valid() bool {
	switch s {
	case ProcessingPending, ProcessingProcessing, ProcessingRetryable, ProcessingProcessed, ProcessingUnreconciledCost, ProcessingTerminalError:
		return true
	default:
		return false
	}
}

// BlocksHoldRelease reports whether an explicit non-settlement hold release would
// strand billable TUR work that still expects atomic settlement to close the hold.
func (s ProcessingStatus) BlocksHoldRelease() bool {
	switch s {
	case ProcessingPending, ProcessingProcessing, ProcessingRetryable, ProcessingUnreconciledCost:
		return true
	default:
		return false
	}
}

// UsageRecordProcessing is mutable worker metadata and is deliberately separate
// from sealed TUR/LUR evidence.
type UsageRecordProcessing struct {
	TURKey         string
	TURFingerprint string
	Status         ProcessingStatus
	LeaseOwner     string
	LeaseUntil     time.Time
	RetryCount     int
	SafeErrorCode  string
	ResultRef      string
	UpdatedAt      time.Time
}

func (p UsageRecordProcessing) Validate() error {
	if strings.TrimSpace(p.TURKey) == "" || strings.TrimSpace(p.TURFingerprint) == "" || !p.Status.Valid() || p.RetryCount < 0 {
		return ErrProcessingInvalid
	}
	return nil
}

// SafeReleaseEligibility proves the inactive-plus-lifetime-plus-grace condition
// required before stale authorization exposure may be released automatically.
type SafeReleaseEligibility struct {
	AlegInactiveAt       time.Time
	AuthorizationCreated time.Time
	Now                  time.Time
	MaximumExecutionLife time.Duration
	SafetyGrace          time.Duration
}

func (e SafeReleaseEligibility) Validate() error {
	if e.AlegInactiveAt.IsZero() || e.AuthorizationCreated.IsZero() || e.Now.IsZero() || e.MaximumExecutionLife <= 0 || e.SafetyGrace < 0 || e.Now.Before(e.AlegInactiveAt) {
		return ErrReleaseNotEligible
	}
	readyAt := e.AuthorizationCreated.Add(e.MaximumExecutionLife).Add(e.SafetyGrace)
	if e.Now.Before(readyAt) {
		return fmt.Errorf("%w: safety grace/lifetime has not elapsed", ErrReleaseNotEligible)
	}
	return nil
}
