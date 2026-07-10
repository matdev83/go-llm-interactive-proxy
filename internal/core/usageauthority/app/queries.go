package app

import (
	"context"
	"errors"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

const maxQueryPageSize = 100

// Status returns the operator-visible accounting authority posture.
func (s *Service) Status(ctx context.Context) (controlplane.AccountingAuthorityStatus, error) {
	status, err := s.statusStatus(ctx)
	if err != nil {
		return controlplane.AccountingAuthorityStatus{}, err
	}
	now := s.now()
	return TranslateAuthorityStatus(status, now), nil
}

// Limits returns bounded live limit rows plus a query state classification.
func (s *Service) Limits(ctx context.Context, q controlplane.AccountingLimitStatusQuery) (LimitStatusResult, error) {
	if q.Limit <= 0 {
		return LimitStatusResult{}, WrapError(ErrInvalidQuery, "query", errors.New("limit must be positive"))
	}
	if q.Limit > maxQueryPageSize {
		return LimitStatusResult{State: QueryStateTooBroad, Page: controlplane.Page[controlplane.AccountingLimitStatusRow]{Visibility: controlplane.VisibilityDefault}}, nil
	}

	status, err := s.statusStatus(ctx)
	if err != nil {
		return LimitStatusResult{}, err
	}
	state := queryStateForStatus(status)
	if state == QueryStateDisabled || state == QueryStateDegraded || state == QueryStateUnavailable {
		return LimitStatusResult{State: state, Page: controlplane.Page[controlplane.AccountingLimitStatusRow]{Visibility: controlplane.VisibilityDefault}}, nil
	}

	if s == nil || s.store == nil {
		return LimitStatusResult{}, WrapError(ErrUnavailable, "limit status", errors.New("store not configured"))
	}
	page, err := s.store.LimitStatus(ctx, q)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return LimitStatusResult{}, WrapError(ErrEvaluationTimeout, "limit status", err)
		}
		return LimitStatusResult{}, WrapError(ErrUnavailable, "limit status", err)
	}
	return LimitStatusResult{State: classifyLimitPage(state, page), Page: page}, nil
}

// Decisions returns bounded live accounting decision rows plus a query state classification.
func (s *Service) Decisions(ctx context.Context, q controlplane.AccountingDecisionQuery) (DecisionHistoryResult, error) {
	if q.Limit <= 0 {
		return DecisionHistoryResult{}, WrapError(ErrInvalidQuery, "query", errors.New("limit must be positive"))
	}
	if q.Limit > maxQueryPageSize {
		return DecisionHistoryResult{State: QueryStateTooBroad, Page: controlplane.Page[controlplane.AccountingDecisionRow]{Visibility: controlplane.VisibilityDefault}}, nil
	}

	status, err := s.statusStatus(ctx)
	if err != nil {
		return DecisionHistoryResult{}, err
	}
	state := queryStateForStatus(status)
	if state == QueryStateDisabled || state == QueryStateDegraded || state == QueryStateUnavailable {
		return DecisionHistoryResult{State: state, Page: controlplane.Page[controlplane.AccountingDecisionRow]{Visibility: controlplane.VisibilityDefault}}, nil
	}

	if s == nil || s.store == nil {
		return DecisionHistoryResult{}, WrapError(ErrUnavailable, "decision history", errors.New("store not configured"))
	}
	page, err := s.store.DecisionHistory(ctx, q)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return DecisionHistoryResult{}, WrapError(ErrEvaluationTimeout, "decision history", err)
		}
		return DecisionHistoryResult{}, WrapError(ErrUnavailable, "decision history", err)
	}
	return DecisionHistoryResult{State: classifyDecisionPage(state, page), Page: page}, nil
}

func (s *Service) statusStatus(ctx context.Context) (domain.AuthorityStatus, error) {
	if s == nil {
		return domain.AuthorityStatus{State: domain.AuthorityStateDisabled, Reason: domain.StatusReasonDisabledByConfig}, nil
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		return domain.AuthorityStatus{}, err
	}
	return s.readiness(ctx, snap.Status)
}

func queryStateForStatus(status domain.AuthorityStatus) QueryState {
	switch status.State {
	case domain.AuthorityStateDisabled:
		return QueryStateDisabled
	case domain.AuthorityStateAdvisoryOnly:
		return QueryStateAdvisoryOnly
	case domain.AuthorityStateDegraded:
		return QueryStateDegraded
	case domain.AuthorityStateUnavailable:
		return QueryStateUnavailable
	default:
		return QueryStateReady
	}
}

func classifyLimitPage(state QueryState, page controlplane.Page[controlplane.AccountingLimitStatusRow]) QueryState {
	if state != QueryStateReady {
		return state
	}
	if len(page.Unsupported) > 0 {
		return QueryStateUnsupported
	}
	if len(page.Items) == 0 {
		return QueryStateEmpty
	}
	return QueryStateReady
}

func classifyDecisionPage(state QueryState, page controlplane.Page[controlplane.AccountingDecisionRow]) QueryState {
	if state != QueryStateReady {
		return state
	}
	if len(page.Unsupported) > 0 {
		return QueryStateUnsupported
	}
	if len(page.Items) == 0 {
		return QueryStateEmpty
	}
	return QueryStateReady
}

func TranslateAuthorityStatus(status domain.AuthorityStatus, now time.Time) controlplane.AccountingAuthorityStatus {
	out := controlplane.AccountingAuthorityStatus{
		LastUpdatedAt:  now.UTC(),
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionSummarized,
	}
	switch status.State {
	case domain.AuthorityStateReady:
		out.State = controlplane.AccountingAuthorityReady
		out.Reason = controlplane.ReasonNone
	case domain.AuthorityStateAdvisoryOnly:
		out.State = controlplane.AccountingAuthorityAdvisoryOnly
		out.Reason = controlplane.ReasonUnsupported
	case domain.AuthorityStateDegraded:
		out.State = controlplane.AccountingAuthorityDegraded
		out.Reason = controlplane.ReasonStoreNotReady
	case domain.AuthorityStateUnavailable:
		out.State = controlplane.AccountingAuthorityUnavailable
		out.Reason = controlplane.ReasonBackingUnavailable
	default:
		out.State = controlplane.AccountingAuthorityDisabled
		out.Reason = controlplane.ReasonDisabled
	}
	return out
}
