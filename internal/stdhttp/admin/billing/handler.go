package billing

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Queries is the read-only billing report port. Implementations must return
// domain values; database handles and provider payloads never cross this edge.
type Queries = corebilling.ReportingStore

// Options configures the bounded billing report and trusted-command handler.
type Options struct {
	Queries         Queries
	Commands        corebilling.AccountProvisioner
	Recovery        corebilling.ExposureRecovery
	DefaultPageSize int
	MaxPageSize     int
}

// NewHandler returns the JSON report surface plus trusted create, funding,
// and credit-policy commands. The caller owns authentication/diagnostics
// protection and path mounting.
func NewHandler(opts Options) http.Handler {
	defaultSize := opts.DefaultPageSize
	if defaultSize <= 0 {
		defaultSize = 100
	}
	maxSize := opts.MaxPageSize
	if maxSize <= 0 {
		maxSize = 1000
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/account", accountHandler(opts, defaultSize, maxSize))
	mux.HandleFunc("/funding", fundingHandler(opts))
	mux.HandleFunc("/credit-policy", creditPolicyHandler(opts))
	mux.HandleFunc("/exposure-repair", exposureRepairHandler(opts))
	// Keep each domain call explicit; no reflection-based report router crosses
	// the protected HTTP boundary.
	mux.HandleFunc("/operator-cost", operatorCostHandler(opts, defaultSize, maxSize))
	mux.HandleFunc("/trial-balance", trialBalanceHandler(opts, defaultSize, maxSize))
	mux.HandleFunc("/exposures", exposuresHandler(opts, defaultSize, maxSize))
	mux.HandleFunc("/call", callHandler(opts))
	mux.HandleFunc("/reconcile-required", reconcileRequiredHandler(opts, defaultSize, maxSize))
	return mux
}

func accountHandler(opts Options, defaultSize, maxSize int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleAccountReport(w, r, opts, defaultSize, maxSize)
		case http.MethodPost:
			handleCreateAccount(w, r, opts)
		default:
			methodNotAllowed(w, http.MethodGet, http.MethodPost)
		}
	}
}

func handleAccountReport(w http.ResponseWriter, r *http.Request, opts Options, defaultSize, maxSize int) {
	if opts.Queries == nil {
		disabled(w)
		return
	}
	page, ok := parsePage(w, r, defaultSize, maxSize)
	if !ok {
		return
	}
	accountID := strings.TrimSpace(r.URL.Query().Get("account_id"))
	if accountID == "" {
		invalid(w)
		return
	}
	result, err := opts.Queries.AccountReport(r.Context(), accountID, page)
	writeResult(w, result, err)
}

func operatorCostHandler(opts Options, defaultSize, maxSize int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkGET(w, r) {
			return
		}
		if opts.Queries == nil {
			disabled(w)
			return
		}
		filter, ok := parseFilter(w, r, defaultSize, maxSize)
		if !ok {
			return
		}
		result, err := opts.Queries.OperatorCostReport(r.Context(), filter)
		writeResult(w, result, err)
	}
}

func trialBalanceHandler(opts Options, defaultSize, maxSize int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkGET(w, r) {
			return
		}
		if opts.Queries == nil {
			disabled(w)
			return
		}
		filter, ok := parseFilter(w, r, defaultSize, maxSize)
		if !ok {
			return
		}
		result, err := opts.Queries.TrialBalanceReport(r.Context(), filter)
		writeResult(w, result, err)
	}
}

func exposuresHandler(opts Options, defaultSize, maxSize int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkGET(w, r) {
			return
		}
		if opts.Queries == nil {
			disabled(w)
			return
		}
		page, ok := parsePage(w, r, defaultSize, maxSize)
		if !ok {
			return
		}
		result, err := opts.Queries.QueryOpenExposures(r.Context(), strings.TrimSpace(r.URL.Query().Get("account_id")), page)
		writeResult(w, result, err)
	}
}

func callHandler(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkGET(w, r) {
			return
		}
		if opts.Queries == nil {
			disabled(w)
			return
		}
		callID := strings.TrimSpace(r.URL.Query().Get("call_id"))
		if callID == "" {
			invalid(w)
			return
		}
		result, err := opts.Queries.CallExplanation(r.Context(), callID)
		writeResult(w, result, err)
	}
}

func reconcileRequiredHandler(opts Options, defaultSize, maxSize int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkGET(w, r) {
			return
		}
		if opts.Queries == nil {
			disabled(w)
			return
		}
		page, ok := parsePage(w, r, defaultSize, maxSize)
		if !ok {
			return
		}
		result, err := opts.Queries.QueryReconcileRequired(r.Context(), page)
		writeResult(w, result, err)
	}
}

func parseFilter(w http.ResponseWriter, r *http.Request, defaultSize, maxSize int) (corebilling.ReportFilter, bool) {
	page, ok := parsePage(w, r, defaultSize, maxSize)
	if !ok {
		return corebilling.ReportFilter{}, false
	}
	from, ok := parseTimeQuery(w, r, "from")
	if !ok {
		return corebilling.ReportFilter{}, false
	}
	to, ok := parseTimeQuery(w, r, "to")
	if !ok {
		return corebilling.ReportFilter{}, false
	}
	return corebilling.ReportFilter{
		AccountID: strings.TrimSpace(r.URL.Query().Get("account_id")),
		Currency:  strings.TrimSpace(r.URL.Query().Get("currency")),
		Book:      corebilling.JournalBook(strings.TrimSpace(r.URL.Query().Get("book"))),
		AfterKey:  strings.TrimSpace(r.URL.Query().Get("after_key")),
		From:      from,
		To:        to,
		Page:      page,
	}, true
}

func parseTimeQuery(w http.ResponseWriter, r *http.Request, name string) (time.Time, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return time.Time{}, true
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		invalid(w)
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func parsePage(w http.ResponseWriter, r *http.Request, defaultSize, maxSize int) (corebilling.PageRequest, bool) {
	q := r.URL.Query()
	page := corebilling.PageRequest{Limit: defaultSize, AfterKey: strings.TrimSpace(q.Get("after_key"))}
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			invalid(w)
			return corebilling.PageRequest{}, false
		}
		page.Limit = value
	}
	if raw := strings.TrimSpace(q.Get("after_sequence")); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			invalid(w)
			return corebilling.PageRequest{}, false
		}
		page.AfterSequence = value
	}
	if page.Limit < 1 || page.Limit > maxSize {
		invalid(w)
		return corebilling.PageRequest{}, false
	}
	return page, true
}

func checkGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return false
	}
	return true
}

func methodNotAllowed(w http.ResponseWriter, allow ...string) {
	w.Header().Set("Allow", strings.Join(allow, ", "))
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
}

func disabled(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "disabled"})
}

func invalid(w http.ResponseWriter) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query"})
}

func writeResult(w http.ResponseWriter, value any, err error) {
	if err != nil {
		switch {
		case errors.Is(err, corebilling.ErrReportInvalid), errors.Is(err, corebilling.ErrMoneyCurrencyMismatch):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query"})
		case errors.Is(err, corebilling.ErrReportNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "billing_report_unavailable"})
		}
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
