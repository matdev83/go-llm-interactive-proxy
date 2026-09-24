package billing

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/jsonbody"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 16.2B protected operator routes for the 16.2A readers plus the
// explicitly opted-in normalized statement import. All routes live under
// the existing configured/protected reports mount; the caller owns
// authentication and path mounting (see mountBillingReports).
//
// Reader absence reports disabled: a nil reader never becomes an
// unauthenticated or cross-scope read. The statement import route registers
// only when both an Importer and an Authorize callback are supplied; either
// missing leaves the route absent. Authorization runs before any body read
// or import call.

// EconomicDetailReader is the durable scoped economic detail port.
type EconomicDetailReader = corebilling.EconomicDetailReader

// EconomicHealthReader is the durable bounded economics health port.
type EconomicHealthReader = corebilling.EconomicHealthReader

// EconomicHealthSink receives the bounded snapshot for observability
// projection. It is an optional, inert port: a nil sink is a no-op and the
// sink never receives raw content or per-request identities.
type EconomicHealthSink func(corebilling.EconomicHealthSnapshot)

// StatementImportAuthorizer authorizes one normalized statement import
// request before its body is read. Any non-nil error rejects the request
// with 403 and the importer is never invoked.
type StatementImportAuthorizer func(*http.Request) error

// StatementImportOptions opts a reports mount into the normalized statement
// import route. Both Importer and Authorize are required; MaxBodyBytes caps
// the batch body and defaults when non-positive.
type StatementImportOptions struct {
	Importer     economics.StatementImporter
	Authorize    StatementImportAuthorizer
	MaxBodyBytes int64
}

func (o *StatementImportOptions) enabled() bool {
	return o != nil && o.Importer != nil && o.Authorize != nil
}

// OperatorReports carries the optional 16.2A operator readers. Nil members
// leave their routes reporting disabled; stock startup leaves the zero
// value, which enables no new route beyond the pre-existing surface.
type OperatorReports struct {
	EconomicDetail  EconomicDetailReader
	Discrepancies   economics.DiscrepancyReader
	Allowances      economics.AllowanceReader
	StatementLines  economics.StatementLineReader
	Adjustments     economics.AdjustmentReader
	Health          EconomicHealthReader
	StatementImport *StatementImportOptions
}

// defaultStatementImportMaxBodyBytes bounds one normalized import batch body.
const defaultStatementImportMaxBodyBytes int64 = 1 << 20

func registerOperatorRoutes(mux *http.ServeMux, opts Options) {
	mux.HandleFunc("/calls/{call_id}/economics", economicDetailCallHandler(opts))
	mux.HandleFunc("/a-legs/{a_leg_id}/economics", economicDetailALegHandler(opts))
	mux.HandleFunc("/reconciliations", discrepanciesHandler(opts))
	mux.HandleFunc("/provider-accounts/{account_id}/allowance-observations", allowanceHandler(opts))
	mux.HandleFunc("/statement-lines", statementLinesHandler(opts))
	mux.HandleFunc("/adjustments", adjustmentsHandler(opts))
	mux.HandleFunc("/health", economicHealthHandler(opts))
	if opts.Operator.StatementImport.enabled() {
		mux.HandleFunc("/statement-observations", statementImportHandler(opts))
	}
}

// economicHealthHandler serves the bounded low-cardinality economics health
// rollup. It returns counts, ages, safe enums and native-currency totals
// only; per-request, per-account and raw-content identifiers never appear.
func economicHealthHandler(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkGET(w, r) {
			return
		}
		if opts.Operator.Health == nil {
			disabled(w)
			return
		}
		result, err := opts.Operator.Health.EconomicHealthSnapshot(r.Context())
		if err == nil && opts.HealthSink != nil {
			opts.HealthSink(result)
		}
		writeResult(w, result, err)
	}
}

func operatorScopeFromQuery(r *http.Request) economics.OperatorScope {
	q := r.URL.Query()
	return economics.OperatorScope{
		StoreID:   strings.TrimSpace(q.Get("store_id")),
		TenantID:  strings.TrimSpace(q.Get("tenant_id")),
		AccountID: strings.TrimSpace(q.Get("account_id")),
	}
}

func operatorLimitFromQuery(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return 0, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		invalid(w)
		return 0, false
	}
	return value, true
}

func economicDetailCallHandler(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkGET(w, r) {
			return
		}
		if opts.Operator.EconomicDetail == nil {
			disabled(w)
			return
		}
		limit, ok := operatorLimitFromQuery(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		scope := operatorScopeFromQuery(r)
		result, err := opts.Operator.EconomicDetail.QueryEconomicDetail(r.Context(), corebilling.EconomicDetailQuery{
			StoreID: scope.StoreID, TenantID: scope.TenantID, AccountID: scope.AccountID,
			BillingCallID: strings.TrimSpace(r.PathValue("call_id")),
			ALegID:        strings.TrimSpace(q.Get("a_leg_id")),
			Limit:         limit,
			Cursor:        strings.TrimSpace(q.Get("cursor")),
		})
		writeResult(w, result, err)
	}
}

func economicDetailALegHandler(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkGET(w, r) {
			return
		}
		if opts.Operator.EconomicDetail == nil {
			disabled(w)
			return
		}
		limit, ok := operatorLimitFromQuery(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		scope := operatorScopeFromQuery(r)
		result, err := opts.Operator.EconomicDetail.QueryEconomicDetail(r.Context(), corebilling.EconomicDetailQuery{
			StoreID: scope.StoreID, TenantID: scope.TenantID, AccountID: scope.AccountID,
			ALegID: strings.TrimSpace(r.PathValue("a_leg_id")),
			Limit:  limit,
			Cursor: strings.TrimSpace(q.Get("cursor")),
		})
		writeResult(w, result, err)
	}
}

func discrepanciesHandler(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkGET(w, r) {
			return
		}
		if opts.Operator.Discrepancies == nil {
			disabled(w)
			return
		}
		limit, ok := operatorLimitFromQuery(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		result, err := opts.Operator.Discrepancies.QueryDiscrepancies(r.Context(), economics.DiscrepancyQuery{
			Scope:       operatorScopeFromQuery(r),
			SubjectKind: metering.SubjectKind(strings.TrimSpace(q.Get("subject_kind"))),
			SubjectID:   strings.TrimSpace(q.Get("subject_id")),
			Limit:       limit,
			Cursor:      strings.TrimSpace(q.Get("cursor")),
		})
		writeResult(w, result, err)
	}
}

func allowanceHandler(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkGET(w, r) {
			return
		}
		if opts.Operator.Allowances == nil {
			disabled(w)
			return
		}
		limit, ok := operatorLimitFromQuery(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		scope := operatorScopeFromQuery(r)
		result, err := opts.Operator.Allowances.QueryAllowances(r.Context(), economics.AllowanceQuery{
			Scope:              scope,
			ProviderAccountKey: strings.TrimSpace(r.PathValue("account_id")),
			PoolID:             strings.TrimSpace(q.Get("pool_id")),
			WindowID:           strings.TrimSpace(q.Get("window_id")),
			Limit:              limit,
			Cursor:             strings.TrimSpace(q.Get("cursor")),
		})
		writeResult(w, result, err)
	}
}

func statementLinesHandler(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkGET(w, r) {
			return
		}
		if opts.Operator.StatementLines == nil {
			disabled(w)
			return
		}
		limit, ok := operatorLimitFromQuery(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		result, err := opts.Operator.StatementLines.QueryStatementLines(r.Context(), economics.StatementLineQuery{
			Scope:              operatorScopeFromQuery(r),
			ProviderAccountKey: strings.TrimSpace(q.Get("provider_account_key")),
			StatementID:        strings.TrimSpace(q.Get("statement_id")),
			PeriodID:           strings.TrimSpace(q.Get("period_id")),
			Outcome:            economics.StatementLineOutcome(strings.TrimSpace(q.Get("outcome"))),
			Limit:              limit,
			Cursor:             strings.TrimSpace(q.Get("cursor")),
		})
		writeResult(w, result, err)
	}
}

func adjustmentsHandler(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkGET(w, r) {
			return
		}
		if opts.Operator.Adjustments == nil {
			disabled(w)
			return
		}
		limit, ok := operatorLimitFromQuery(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		scope := operatorScopeFromQuery(r)
		result, err := opts.Operator.Adjustments.QueryAdjustments(r.Context(), economics.AdjustmentQuery{
			Scope:   scope,
			CallID:  strings.TrimSpace(q.Get("call_id")),
			HeadKey: strings.TrimSpace(q.Get("head_key")),
			Limit:   limit,
			Cursor:  strings.TrimSpace(q.Get("cursor")),
		})
		writeResult(w, result, err)
	}
}

func statementImportHandler(opts Options) http.HandlerFunc {
	imp := opts.Operator.StatementImport
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if err := imp.Authorize(r); err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		if contentType := r.Header.Get("Content-Type"); !strings.Contains(strings.ToLower(contentType), "application/json") {
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "unsupported_media_type"})
			return
		}
		maxBytes := imp.MaxBodyBytes
		if maxBytes <= 0 {
			maxBytes = defaultStatementImportMaxBodyBytes
		}
		var batch economics.StatementBatch
		if err := jsonbody.Decode(w, r, &batch, jsonbody.Policy{MaxBytes: maxBytes}); err != nil {
			if errors.Is(err, jsonbody.ErrTooLarge) {
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request_too_large"})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_command"})
			return
		}
		if err := batch.Validate(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_command"})
			return
		}
		result, err := imp.Importer.Import(r.Context(), batch)
		if err != nil {
			writeImportError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func writeImportError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, corebilling.ErrStatementImportScopeMismatch):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
	case isStatementImportConflict(err):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "conflict"})
	case errors.Is(err, corebilling.ErrStatementImportInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_command"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "billing_command_unavailable"})
	}
}

func isStatementImportConflict(err error) bool {
	var conflict *corebilling.StatementImportConflictError
	return errors.As(err, &conflict)
}
