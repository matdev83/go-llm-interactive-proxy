package billing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

type recordingQueries struct {
	account   corebilling.AccountReport
	call      corebilling.CallExplanation
	operator  corebilling.OperatorCostReport
	trial     corebilling.TrialBalanceReport
	exposures corebilling.ExposurePage
	reconcile corebilling.AccountStatePage
	err       error

	lastAccountID string
	lastCallID    string
	lastPage      corebilling.PageRequest
	lastFilter    corebilling.ReportFilter
}

func (s *recordingQueries) AccountReport(_ context.Context, accountID string, page corebilling.PageRequest) (corebilling.AccountReport, error) {
	s.lastAccountID, s.lastPage = accountID, page
	return s.account, s.err
}

func (s *recordingQueries) CallExplanation(_ context.Context, callID string) (corebilling.CallExplanation, error) {
	s.lastCallID = callID
	return s.call, s.err
}

func (s *recordingQueries) OperatorCostReport(_ context.Context, filter corebilling.ReportFilter) (corebilling.OperatorCostReport, error) {
	s.lastFilter = filter
	return s.operator, s.err
}

func (s *recordingQueries) TrialBalanceReport(_ context.Context, filter corebilling.ReportFilter) (corebilling.TrialBalanceReport, error) {
	s.lastFilter = filter
	return s.trial, s.err
}

func (s *recordingQueries) QueryOpenExposures(_ context.Context, accountID string, page corebilling.PageRequest) (corebilling.ExposurePage, error) {
	s.lastAccountID, s.lastPage = accountID, page
	return s.exposures, s.err
}

func (s *recordingQueries) QueryReconcileRequired(_ context.Context, page corebilling.PageRequest) (corebilling.AccountStatePage, error) {
	s.lastPage = page
	return s.reconcile, s.err
}

func TestBillingHandlerDisabledWhenQueriesNil(t *testing.T) {
	t.Parallel()
	h := NewHandler(Options{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/account?account_id=acct", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rec.Code)
	}
	assertJSONError(t, rec, "disabled")
}

func TestBillingHandlerMethodNotAllowed(t *testing.T) {
	t.Parallel()
	h := NewHandler(Options{Queries: &recordingQueries{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/call?call_id=bc_1", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
	if rec.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("Allow=%q", rec.Header().Get("Allow"))
	}
	assertJSONError(t, rec, "method_not_allowed")
}

func TestBillingHandlerRejectsInvalidPagination(t *testing.T) {
	t.Parallel()
	h := NewHandler(Options{Queries: &recordingQueries{}})
	for _, path := range []string{
		"/account?account_id=acct&limit=0",
		"/account?account_id=acct&limit=1001",
		"/account?account_id=acct&after_sequence=nope",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
			}
			assertJSONError(t, rec, "invalid_query")
		})
	}
}

func TestBillingHandlerMapsDomainAndUnknownErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "not found", err: corebilling.ErrReportNotFound, status: http.StatusNotFound, code: "not_found"},
		{name: "invalid", err: corebilling.ErrReportInvalid, status: http.StatusBadRequest, code: "invalid_query"},
		{name: "currency mismatch", err: corebilling.ErrMoneyCurrencyMismatch, status: http.StatusBadRequest, code: "invalid_query"},
		{name: "unknown store error", err: errors.New("db down"), status: http.StatusInternalServerError, code: "billing_report_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := NewHandler(Options{Queries: &recordingQueries{err: tt.err}})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/call?call_id=bc_1", nil))
			if rec.Code != tt.status {
				t.Fatalf("status=%d want %d body=%s", rec.Code, tt.status, rec.Body.String())
			}
			assertJSONError(t, rec, tt.code)
		})
	}
}

func TestBillingHandlerForwardsQueryContracts(t *testing.T) {
	t.Parallel()
	from := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)

	t.Run("operator-cost", func(t *testing.T) {
		t.Parallel()
		q := &recordingQueries{
			operator:  corebilling.OperatorCostReport{CustomerRevenue: corebilling.Money{Nano: 9, Currency: "USD"}},
			trial:     corebilling.TrialBalanceReport{AccountID: "acct", Balanced: true},
			reconcile: corebilling.AccountStatePage{Items: []corebilling.Account{{ID: "acct"}}},
		}
		h := NewHandler(Options{Queries: q, DefaultPageSize: 25, MaxPageSize: 500})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/operator-cost?account_id=acct&limit=10&after_sequence=5&after_key=k1&book=financial&from=2020-01-01T00:00:00Z&to=2020-01-02T00:00:00Z&currency=USD", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var payload corebilling.OperatorCostReport
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.CustomerRevenue.Nano != 9 {
			t.Fatalf("payload = %+v", payload)
		}
		if q.lastFilter.AccountID != "acct" || q.lastFilter.Currency != "USD" || q.lastFilter.Book != corebilling.JournalBookFinancial {
			t.Fatalf("filter = %+v", q.lastFilter)
		}
		if q.lastFilter.Page.Limit != 10 || q.lastFilter.Page.AfterSequence != 5 || q.lastFilter.Page.AfterKey != "k1" {
			t.Fatalf("page = %+v", q.lastFilter.Page)
		}
		if !q.lastFilter.From.Equal(from) || !q.lastFilter.To.Equal(to) {
			t.Fatalf("time range from=%v to=%v", q.lastFilter.From, q.lastFilter.To)
		}
	})
	t.Run("trial-balance", func(t *testing.T) {
		t.Parallel()
		q := &recordingQueries{
			operator:  corebilling.OperatorCostReport{CustomerRevenue: corebilling.Money{Nano: 9, Currency: "USD"}},
			trial:     corebilling.TrialBalanceReport{AccountID: "acct", Balanced: true},
			reconcile: corebilling.AccountStatePage{Items: []corebilling.Account{{ID: "acct"}}},
		}
		h := NewHandler(Options{Queries: q, DefaultPageSize: 25, MaxPageSize: 500})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/trial-balance?account_id=acct", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var payload corebilling.TrialBalanceReport
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if !payload.Balanced || payload.AccountID != "acct" {
			t.Fatalf("payload = %+v", payload)
		}
	})
	t.Run("reconcile-required", func(t *testing.T) {
		t.Parallel()
		q := &recordingQueries{
			operator:  corebilling.OperatorCostReport{CustomerRevenue: corebilling.Money{Nano: 9, Currency: "USD"}},
			trial:     corebilling.TrialBalanceReport{AccountID: "acct", Balanced: true},
			reconcile: corebilling.AccountStatePage{Items: []corebilling.Account{{ID: "acct"}}},
		}
		h := NewHandler(Options{Queries: q, DefaultPageSize: 25, MaxPageSize: 500})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/reconcile-required?limit=4", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if q.lastPage.Limit != 4 {
			t.Fatalf("page = %+v", q.lastPage)
		}
		var payload corebilling.AccountStatePage
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Items) != 1 || payload.Items[0].ID != "acct" {
			t.Fatalf("payload = %+v", payload)
		}
	})
}

func TestBillingHandlerExposesCallPathDiagnostics(t *testing.T) {
	t.Parallel()

	t.Run("exposures", func(t *testing.T) {
		t.Parallel()
		q := &recordingQueries{
			exposures: corebilling.ExposurePage{Items: []corebilling.ExposureReport{{CallID: "bc_1", AccountID: "acct"}}},
			call:      corebilling.CallExplanation{CallID: "bc_1", Result: corebilling.TurnResultSummary{Processed: true}},
		}
		h := NewHandler(Options{Queries: q, DefaultPageSize: 25, MaxPageSize: 500})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/exposures?account_id=acct&limit=7&after_key=k0", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if q.lastAccountID != "acct" || q.lastPage.Limit != 7 || q.lastPage.AfterKey != "k0" {
			t.Fatalf("forwarded page = account=%q page=%+v", q.lastAccountID, q.lastPage)
		}
		var payload corebilling.ExposurePage
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Items) != 1 || payload.Items[0].CallID != "bc_1" {
			t.Fatalf("payload = %+v", payload)
		}
	})
	t.Run("call", func(t *testing.T) {
		t.Parallel()
		q := &recordingQueries{
			exposures: corebilling.ExposurePage{Items: []corebilling.ExposureReport{{CallID: "bc_1", AccountID: "acct"}}},
			call:      corebilling.CallExplanation{CallID: "bc_1", Result: corebilling.TurnResultSummary{Processed: true}},
		}
		h := NewHandler(Options{Queries: q, DefaultPageSize: 25, MaxPageSize: 500})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/call?call_id=bc_1", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if q.lastCallID != "bc_1" {
			t.Fatalf("call id = %q", q.lastCallID)
		}
		var payload corebilling.CallExplanation
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.CallID != "bc_1" || !payload.Result.Processed {
			t.Fatalf("payload = %+v", payload)
		}
	})
}

func assertJSONError(t *testing.T, body *httptest.ResponseRecorder, want string) {
	t.Helper()
	assertJSONErrorReader(t, body.Body, want)
}

func assertJSONErrorReader(t *testing.T, r io.Reader, want string) {
	t.Helper()
	var payload map[string]string
	if err := json.NewDecoder(r).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["error"] != want {
		t.Fatalf("error=%q want %q payload=%v", payload["error"], want, payload)
	}
}
