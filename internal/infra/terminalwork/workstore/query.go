package workstore

import (
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

type Query struct {
	WorkID        string
	SourceKey     terminalwork.SourceKey
	State         sdk.WorkState
	States        []sdk.WorkState
	ProviderID    string
	Kind          sdk.WorkKind
	RequestID     string
	AttemptID     string
	DueBefore     time.Time
	UpdatedAfter  time.Time
	UpdatedBefore time.Time
	Limit         int
	Cursor        string
}

type Page struct {
	Records []terminalwork.WorkRecord
	Cursor  string
}

type ClaimDueCommand struct {
	OwnerID    string
	TTL        time.Duration
	Limit      int
	Now        time.Time
	ProviderID string
	Kind       sdk.WorkKind
}

type RenewClaimCommand struct {
	WorkID  string
	OwnerID string
	TTL     time.Duration
	Now     time.Time
}

type CompleteCommand struct {
	WorkID          string
	ExpectedOwnerID string
	Now             time.Time
}

type ScheduleRetryCommand struct {
	WorkID          string
	ExpectedOwnerID string
	Schedule        terminalwork.RetrySchedule
	Err             terminalwork.BoundedError
	Now             time.Time
}

type QuarantineCommand struct {
	WorkID string
	Err    terminalwork.BoundedError
	Now    time.Time
}

type PromotePendingCommand struct {
	WorkID string
	Now    time.Time
}

func ValidateQuery(q Query) error {
	if !HasSelectiveBound(q) {
		return ErrQueryTooBroad
	}
	if q.Limit > MaxQueryLimit {
		return ErrQueryLimitExceeded
	}
	return nil
}

func HasSelectiveBound(q Query) bool {
	if strings.TrimSpace(q.WorkID) != "" {
		return true
	}
	if err := q.SourceKey.Validate(); err == nil {
		return true
	}
	if strings.TrimSpace(q.RequestID) != "" ||
		strings.TrimSpace(q.AttemptID) != "" ||
		strings.TrimSpace(q.ProviderID) != "" {
		return true
	}
	if q.Kind != "" && q.Kind.IsKnown() {
		return true
	}
	if q.State != "" && q.State.IsKnown() {
		return true
	}
	if len(q.States) > 0 {
		return true
	}
	if !q.DueBefore.IsZero() {
		return true
	}
	if !q.UpdatedAfter.IsZero() || !q.UpdatedBefore.IsZero() {
		return true
	}
	return false
}

func recordMatchesQuery(r terminalwork.WorkRecord, q Query) bool {
	if workID := strings.TrimSpace(q.WorkID); workID != "" && r.WorkID != workID {
		return false
	}
	if err := q.SourceKey.Validate(); err == nil && r.SourceKey != q.SourceKey {
		return false
	}
	if q.State != "" && r.State != q.State {
		return false
	}
	if len(q.States) > 0 {
		match := false
		for _, st := range q.States {
			if r.State == st {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}
	if provider := strings.TrimSpace(q.ProviderID); provider != "" && r.ProviderID != provider {
		return false
	}
	if q.Kind != "" && r.Kind != q.Kind {
		return false
	}
	if req := strings.TrimSpace(q.RequestID); req != "" && r.Lifecycle.RequestID != req {
		return false
	}
	if att := strings.TrimSpace(q.AttemptID); att != "" && r.Lifecycle.AttemptID != att {
		return false
	}
	if !q.DueBefore.IsZero() {
		switch r.State {
		case sdk.WorkStatePending:
		case sdk.WorkStateRetry:
			if r.NextRetryAt.After(q.DueBefore) {
				return false
			}
		case sdk.WorkStateClaimed:
			if r.Lease.HeldAt(q.DueBefore) {
				return false
			}
		default:
			return false
		}
	}
	if !q.UpdatedAfter.IsZero() && r.UpdatedAt.Before(q.UpdatedAfter) {
		return false
	}
	if !q.UpdatedBefore.IsZero() && r.UpdatedAt.After(q.UpdatedBefore) {
		return false
	}
	return true
}

func isDueForClaim(r terminalwork.WorkRecord, now time.Time) bool {
	switch r.State {
	case sdk.WorkStatePending:
		return true
	case sdk.WorkStateRetry:
		return !now.Before(r.NextRetryAt)
	case sdk.WorkStateClaimed:
		return !r.Lease.HeldAt(now)
	default:
		return false
	}
}
