package controlplane

import (
	"context"
	"errors"
	"net/http"
	"strings"

	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

// AuthorityOptions configures the protected accounting-authority HTTP handler.
type AuthorityOptions struct {
	Queries         authorityQueries
	DefaultPageSize int
	MaxPageSize     int
}

type authorityQueries interface {
	Status(ctx context.Context) (cp.AccountingAuthorityStatus, error)
	Limits(ctx context.Context, q cp.AccountingLimitStatusQuery) (authorityapp.LimitStatusResult, error)
	Decisions(ctx context.Context, q cp.AccountingDecisionQuery) (authorityapp.DecisionHistoryResult, error)
}

// NewAccountingAuthorityHandler returns a protected HTTP handler for live
// authority status and bounded query views.
func NewAccountingAuthorityHandler(opts AuthorityOptions) http.Handler {
	queries := opts.Queries
	defaultPageSize := opts.DefaultPageSize
	if defaultPageSize <= 0 {
		defaultPageSize = 100
	}
	maxPageSize := opts.MaxPageSize
	if maxPageSize <= 0 {
		maxPageSize = 100
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if queries == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "disabled"})
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if queries == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "disabled"})
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		status, err := queries.Status(r.Context())
		if err != nil {
			writeAuthorityQueryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	})
	mux.HandleFunc("/limits", func(w http.ResponseWriter, r *http.Request) {
		serveAuthorityPage(w, r, queries, defaultPageSize, maxPageSize, func(limit int) (authorityapp.QueryState, cp.Page[cp.AccountingLimitStatusRow], error) {
			q := cp.AccountingLimitStatusQuery{
				Common:     parseCommonFilters(r),
				RuleID:     strings.TrimSpace(r.URL.Query().Get("rule_id")),
				Unit:       strings.TrimSpace(r.URL.Query().Get("unit")),
				Currency:   strings.TrimSpace(r.URL.Query().Get("currency")),
				Authority:  cp.AccountingAuthoritySource(strings.TrimSpace(r.URL.Query().Get("authority"))),
				Limit:      limit,
				Cursor:     cp.Cursor{Token: strings.TrimSpace(r.URL.Query().Get("cursor"))},
				Visibility: cp.Visibility(strings.TrimSpace(r.URL.Query().Get("visibility"))),
			}
			if raw := strings.TrimSpace(r.URL.Query().Get("settlement_state")); raw != "" {
				q.SettlementState = cp.AccountingSettlementState(raw)
			}
			if raw := strings.TrimSpace(r.URL.Query().Get("evidence_state")); raw != "" {
				q.EvidenceState = cp.EvidenceState(raw)
			}
			if raw := strings.TrimSpace(r.URL.Query().Get("redaction_state")); raw != "" {
				q.RedactionState = cp.RedactionState(raw)
			}
			res, err := queries.Limits(r.Context(), q)
			if err != nil {
				return "", cp.Page[cp.AccountingLimitStatusRow]{}, err
			}
			return res.State, res.Page, nil
		})
	})
	mux.HandleFunc("/decision-history", func(w http.ResponseWriter, r *http.Request) {
		serveAuthorityPage(w, r, queries, defaultPageSize, maxPageSize, func(limit int) (authorityapp.QueryState, cp.Page[cp.AccountingDecisionRow], error) {
			q := cp.AccountingDecisionQuery{
				Common:     parseCommonFilters(r),
				RuleID:     strings.TrimSpace(r.URL.Query().Get("rule_id")),
				Unit:       strings.TrimSpace(r.URL.Query().Get("unit")),
				Currency:   strings.TrimSpace(r.URL.Query().Get("currency")),
				Authority:  cp.AccountingAuthoritySource(strings.TrimSpace(r.URL.Query().Get("authority"))),
				Limit:      limit,
				Cursor:     cp.Cursor{Token: strings.TrimSpace(r.URL.Query().Get("cursor"))},
				Visibility: cp.Visibility(strings.TrimSpace(r.URL.Query().Get("visibility"))),
			}
			if raw := strings.TrimSpace(r.URL.Query().Get("settlement_state")); raw != "" {
				q.SettlementState = cp.AccountingSettlementState(raw)
			}
			if raw := strings.TrimSpace(r.URL.Query().Get("evidence_state")); raw != "" {
				q.EvidenceState = cp.EvidenceState(raw)
			}
			if raw := strings.TrimSpace(r.URL.Query().Get("redaction_state")); raw != "" {
				q.RedactionState = cp.RedactionState(raw)
			}
			res, err := queries.Decisions(r.Context(), q)
			if err != nil {
				return "", cp.Page[cp.AccountingDecisionRow]{}, err
			}
			return res.State, res.Page, nil
		})
	})
	return mux
}

type authorityPageResponse[T any] struct {
	State authorityapp.QueryState `json:"state"`
	Page  cp.Page[T]              `json:"page"`
}

func serveAuthorityPage[T any](w http.ResponseWriter, r *http.Request, queries authorityQueries, defaultLimit, maxLimit int, call func(limit int) (authorityapp.QueryState, cp.Page[T], error)) {
	if queries == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "disabled"})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	limit, err := parseLimit(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query"})
		return
	}
	if limit == 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "too_broad"})
		return
	}
	state, page, err := call(limit)
	if err != nil {
		writeAuthorityQueryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, authorityPageResponse[T]{State: state, Page: page})
}

func writeAuthorityQueryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authorityapp.ErrDisabled):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "disabled"})
	case errors.Is(err, authorityapp.ErrUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
	case errors.Is(err, authorityapp.ErrDegraded):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "degraded"})
	case errors.Is(err, authorityapp.ErrInvalidQuery):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query"})
	case errors.Is(err, authorityapp.ErrUnsupportedFilter):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_filter"})
	case errors.Is(err, authorityapp.ErrEvaluationTimeout):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
	default:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
	}
}
