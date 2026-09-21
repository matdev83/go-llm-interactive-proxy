package billing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 16.2B RED contract: protected operator routes for the 16.2A readers
// plus the explicitly opted-in statement import. Tests use stub readers and
// a counting importer; auth runs before any body read or import call.

type stubEconomicDetail struct {
	detail corebilling.EconomicDetail
	err    error
}

func (s stubEconomicDetail) QueryEconomicDetail(context.Context, corebilling.EconomicDetailQuery) (corebilling.EconomicDetail, error) {
	return s.detail, s.err
}

type stubDiscrepancies struct {
	page economics.DiscrepancyPage
	err  error
}

func (s stubDiscrepancies) QueryDiscrepancies(context.Context, economics.DiscrepancyQuery) (economics.DiscrepancyPage, error) {
	return s.page, s.err
}

type stubAllowances struct {
	page economics.AllowancePage
	err  error
}

func (s stubAllowances) QueryAllowances(context.Context, economics.AllowanceQuery) (economics.AllowancePage, error) {
	return s.page, s.err
}

type stubStatementLines struct {
	page economics.StatementLinePage
	err  error
}

func (s stubStatementLines) QueryStatementLines(context.Context, economics.StatementLineQuery) (economics.StatementLinePage, error) {
	return s.page, s.err
}

type stubAdjustments struct {
	page economics.AdjustmentPage
	err  error
}

func (s stubAdjustments) QueryAdjustments(context.Context, economics.AdjustmentQuery) (economics.AdjustmentPage, error) {
	return s.page, s.err
}

type countingImporter struct {
	calls  int
	result economics.ImportResult
	err    error
}

func (c *countingImporter) Import(context.Context, economics.StatementBatch) (economics.ImportResult, error) {
	c.calls++
	return c.result, c.err
}

type recordingAuthorizer struct {
	calls int
	err   error
}

func (a *recordingAuthorizer) authorize(*http.Request) error {
	a.calls++
	return a.err
}

func operatorTestOptions() Options {
	return Options{
		Queries: &recordingQueries{},
		Operator: OperatorReports{
			EconomicDetail: stubEconomicDetail{detail: corebilling.EconomicDetail{Truncated: false}},
			Discrepancies:  stubDiscrepancies{page: economics.DiscrepancyPage{}},
			Allowances:     stubAllowances{page: economics.AllowancePage{}},
			StatementLines: stubStatementLines{page: economics.StatementLinePage{}},
			Adjustments:    stubAdjustments{page: economics.AdjustmentPage{}},
		},
	}
}

func TestOperatorCallDetailRoute(t *testing.T) {
	t.Parallel()
	h := NewHandler(operatorTestOptions())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/calls/bc_1/economics?store_id=test&account_id=acct", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["scope"]; !ok {
		t.Fatalf("detail payload lacks scope: %v", payload)
	}
}

func TestOperatorALegDetailRoute(t *testing.T) {
	t.Parallel()
	h := NewHandler(operatorTestOptions())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a-legs/a-1/economics?store_id=test&account_id=acct&limit=10", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestOperatorDiscrepancyRoute(t *testing.T) {
	t.Parallel()
	h := NewHandler(operatorTestOptions())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/reconciliations?store_id=test&tenant_id=t&limit=5", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

// Phase 16 second-pass Finding 5 wire contract: an aggregate-only retained
// reconciliation serializes its bounded classification and missing-evidence
// identity without inventing quantity or monetary fields.
func TestOperatorDiscrepancyAggregateOnlyWire(t *testing.T) {
	t.Parallel()
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: "test", TenantID: "t",
		ALegID: "a-1", BillingCallID: "bc_" + strings.Repeat("a", 32), BLegID: "b-1",
	}
	page := economics.DiscrepancyPage{Items: []economics.DiscrepancyView{{
		ID: "recon-agg-wire", Revision: 1, Subject: subject,
		PolicyID: "policy", PolicyVersion: "v1",
		Aggregate: &economics.DiscrepancyAggregate{
			Status: economics.DiscrepancyMissingProvider, MissingIDs: []string{"end-to-end:USD"},
		},
		CreatedAt: time.Unix(1_700_030_000, 0).UTC(),
	}}}
	if err := page.Validate(); err != nil {
		t.Fatalf("aggregate-only page must validate: %v", err)
	}
	options := operatorTestOptions()
	options.Operator.Discrepancies = stubDiscrepancies{page: page}
	h := NewHandler(options)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/reconciliations?store_id=test&tenant_id=t", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []struct {
			QuantityStatus string                          `json:"quantity_status"`
			MonetaryState  string                          `json:"monetary_state"`
			Aggregate      *economics.DiscrepancyAggregate `json:"aggregate"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items=%d want 1", len(payload.Items))
	}
	if payload.Items[0].QuantityStatus != "" || payload.Items[0].MonetaryState != "" {
		t.Fatalf("aggregate-only row must not invent planes: %+v", payload.Items[0])
	}
	if payload.Items[0].Aggregate == nil || payload.Items[0].Aggregate.Status != economics.DiscrepancyMissingProvider {
		t.Fatalf("aggregate wire fields lost: %+v", payload.Items[0])
	}
	if len(payload.Items[0].Aggregate.MissingIDs) != 1 || payload.Items[0].Aggregate.MissingIDs[0] != "end-to-end:USD" {
		t.Fatalf("aggregate missing evidence lost: %+v", payload.Items[0].Aggregate)
	}
}

func TestOperatorAllowanceRoute(t *testing.T) {
	t.Parallel()
	h := NewHandler(operatorTestOptions())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/provider-accounts/provider-1/allowance-observations?store_id=test&tenant_id=t", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestOperatorStatementLinesRoute(t *testing.T) {
	t.Parallel()
	h := NewHandler(operatorTestOptions())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/statement-lines?store_id=test&tenant_id=t&provider_account_key=p", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestOperatorAdjustmentsRoute(t *testing.T) {
	t.Parallel()
	h := NewHandler(operatorTestOptions())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/adjustments?store_id=test&account_id=acct", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestOperatorRoutesDisabledWhenReadersNil(t *testing.T) {
	t.Parallel()
	h := NewHandler(Options{Queries: &recordingQueries{}})
	for _, path := range []string{
		"/calls/bc_1/economics?store_id=test&account_id=acct",
		"/a-legs/a-1/economics?store_id=test&account_id=acct",
		"/reconciliations?store_id=test&tenant_id=t",
		"/provider-accounts/p/allowance-observations?store_id=test&tenant_id=t",
		"/statement-lines?store_id=test&tenant_id=t&provider_account_key=p",
		"/adjustments?store_id=test&account_id=acct",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("path %s status=%d want 404 disabled", path, rec.Code)
		}
		assertJSONError(t, rec, "disabled")
	}
}

func TestOperatorRoutesMapErrors(t *testing.T) {
	t.Parallel()
	t.Run("invalid query is 400", func(t *testing.T) {
		t.Parallel()
		h := NewHandler(Options{Queries: &recordingQueries{}, Operator: OperatorReports{Discrepancies: stubDiscrepancies{err: economics.ErrOperatorQueryInvalid}}})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/reconciliations?store_id=test&tenant_id=t", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400", rec.Code)
		}
		assertJSONError(t, rec, "invalid_query")
	})
	t.Run("scope mismatch is 404 without oracle", func(t *testing.T) {
		t.Parallel()
		h := NewHandler(Options{Queries: &recordingQueries{}, Operator: OperatorReports{Discrepancies: stubDiscrepancies{err: economics.ErrOperatorScopeMismatch}}})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/reconciliations?store_id=test&tenant_id=t", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status=%d want 404", rec.Code)
		}
		assertJSONError(t, rec, "not_found")
	})
	t.Run("bad cursor is 400", func(t *testing.T) {
		t.Parallel()
		h := NewHandler(Options{Queries: &recordingQueries{}, Operator: OperatorReports{Adjustments: stubAdjustments{err: economics.ErrOperatorCursorInvalid}}})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/adjustments?store_id=test&account_id=acct&cursor=bogus", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400", rec.Code)
		}
		assertJSONError(t, rec, "invalid_query")
	})
	t.Run("method mismatch is 405", func(t *testing.T) {
		t.Parallel()
		h := NewHandler(operatorTestOptions())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/reconciliations?store_id=test&tenant_id=t", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d want 405", rec.Code)
		}
	})
}

func TestStatementImportAbsentByDefault(t *testing.T) {
	t.Parallel()
	h := NewHandler(operatorTestOptions())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/statement-observations", strings.NewReader(`{}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 when import is not opted in", rec.Code)
	}
}

func TestStatementImportRequiresBothImporterAndAuthorizer(t *testing.T) {
	t.Parallel()
	importer := &countingImporter{}
	for _, tc := range []struct {
		name string
		opts StatementImportOptions
	}{
		{name: "importer only", opts: StatementImportOptions{Importer: importer}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options := operatorTestOptions()
			options.Operator.StatementImport = &tc.opts
			h := NewHandler(options)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/statement-observations", strings.NewReader(`{}`)))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status=%d want 404 without authorizer", rec.Code)
			}
			if importer.calls != 0 {
				t.Fatalf("importer calls=%d want 0", importer.calls)
			}
		})
	}
	auth := &recordingAuthorizer{}
	options := operatorTestOptions()
	options.Operator.StatementImport = &StatementImportOptions{Authorize: auth.authorize}
	h := NewHandler(options)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/statement-observations", strings.NewReader(`{}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 without importer", rec.Code)
	}
	if auth.calls != 0 {
		t.Fatalf("authorizer calls=%d want 0 when route is absent", auth.calls)
	}
}

type countingBody struct {
	io.Reader
	reads int
}

func (b *countingBody) Read(p []byte) (int, error) {
	b.reads++
	return b.Reader.Read(p)
}

func importRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/statement-observations", &countingBody{Reader: strings.NewReader(body)})
	req.Header.Set("Content-Type", "application/json")
	return req
}

func importTestBatch(t *testing.T) string {
	t.Helper()
	subject := map[string]any{
		"kind": "statement_line", "store_id": "test", "provider_account_key": "p",
		"statement_id": "s", "statement_line_id": "line-1", "period_id": "2026-09",
	}
	batch := map[string]any{
		"version": 1, "provider_account_key": "p", "statement_id": "s",
		"revision": 1, "period_id": "2026-09", "subject": subject,
		"observations": []any{map[string]any{
			"version": 2, "id": "obs-1", "source_event_key": "event-1", "revision": 1,
			"stream_id": "stream-1", "sequence": 1,
			"origin": "statement", "acquisition": "statement_importer", "authority": "verified_statement",
			"perspective": "operator", "boundary": "backend_ingress", "lifecycle": "backend_attempt",
			"subject": subject, "correlation": map[string]any{"store_id": "test"},
			"semantics": "delta", "observed_at": "2026-09-01T00:00:00Z", "received_at": "2026-09-01T00:00:00Z",
			"mapping_ref": "test.v1",
			"measures": []any{map[string]any{
				"key": map[string]any{
					"direction": "input", "component": "input_token",
					"unit": "token", "schema_id": "lip.default.token_inclusion.v1",
				},
				"value":   map[string]any{"coefficient": "10", "scale": 0},
				"quality": "observed",
			}},
			"charges": []any{map[string]any{
				"charge_item_id": "charge-1",
				"component": map[string]any{
					"direction": "input", "component": "input_token",
					"unit": "token", "schema_id": "lip.default.token_inclusion.v1",
				},
				"amount":   map[string]any{"coefficient": "10", "scale": 0},
				"currency": "USD", "kind": "component",
			}},
		}},
		"lines": []any{map[string]any{
			"id": "line-1", "revision": 1, "subject": subject,
			"observation": map[string]any{
				"store_id": "test", "observation_id": "obs-1", "revision": 1,
				"payload_hash": strings.Repeat("c", 64),
			},
			"charge_item_id": "charge-1",
		}},
	}
	// The line linkage requires the observation ref hash to match the
	// included observation fingerprint exactly.
	observations := batch["observations"].([]any)
	obsPayload, err := json.Marshal(observations[0])
	if err != nil {
		t.Fatal(err)
	}
	var observation metering.Observation
	if err := json.Unmarshal(obsPayload, &observation); err != nil {
		t.Fatal(err)
	}
	lines := batch["lines"].([]any)
	line := lines[0].(map[string]any)
	ref := line["observation"].(map[string]any)
	ref["payload_hash"] = observation.Fingerprint()
	payload, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func TestStatementImportUnauthorizedReadsNothing(t *testing.T) {
	t.Parallel()
	importer := &countingImporter{}
	auth := &recordingAuthorizer{err: errors.New("denied")}
	options := operatorTestOptions()
	options.Operator.StatementImport = &StatementImportOptions{Importer: importer, Authorize: auth.authorize}
	h := NewHandler(options)

	body := &countingBody{Reader: strings.NewReader(`{"version":1}`)}
	req := httptest.NewRequest(http.MethodPost, "/statement-observations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rec.Code)
	}
	if auth.calls != 1 {
		t.Fatalf("authorizer calls=%d want 1", auth.calls)
	}
	if body.reads != 0 {
		t.Fatalf("body reads=%d want 0 before authorization", body.reads)
	}
	if importer.calls != 0 {
		t.Fatalf("importer calls=%d want 0 when unauthorized", importer.calls)
	}
}

func TestStatementImportAuthorizedResult(t *testing.T) {
	t.Parallel()
	importer := &countingImporter{result: economics.ImportResult{Accepted: []string{"line-1"}, Replayed: []string{"line-0"}, Unmatched: []string{"line-9"}}}
	auth := &recordingAuthorizer{}
	options := operatorTestOptions()
	options.Operator.StatementImport = &StatementImportOptions{Importer: importer, Authorize: auth.authorize}
	h := NewHandler(options)

	batch := importTestBatch(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, importRequest(t, batch))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if importer.calls != 1 {
		t.Fatalf("importer calls=%d want exactly 1", importer.calls)
	}
	var result economics.ImportResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Accepted) != 1 || len(result.Replayed) != 1 || len(result.Unmatched) != 1 {
		t.Fatalf("import result not mapped: %+v", result)
	}
}

func TestStatementImportRejectsBadBodies(t *testing.T) {
	t.Parallel()
	newHandler := func() (*countingImporter, http.Handler) {
		importer := &countingImporter{}
		auth := &recordingAuthorizer{}
		options := operatorTestOptions()
		options.Operator.StatementImport = &StatementImportOptions{Importer: importer, Authorize: auth.authorize, MaxBodyBytes: 64}
		return importer, NewHandler(options)
	}
	t.Run("wrong content type is 415", func(t *testing.T) {
		t.Parallel()
		importer, h := newHandler()
		req := httptest.NewRequest(http.MethodPost, "/statement-observations", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("status=%d want 415", rec.Code)
		}
		if importer.calls != 0 {
			t.Fatalf("importer calls=%d want 0", importer.calls)
		}
	})
	t.Run("oversized body is 413", func(t *testing.T) {
		t.Parallel()
		importer, h := newHandler()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, importRequest(t, strings.Repeat(" ", 128)))
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d want 413", rec.Code)
		}
		if importer.calls != 0 {
			t.Fatalf("importer calls=%d want 0", importer.calls)
		}
	})
	t.Run("invalid json is 400", func(t *testing.T) {
		t.Parallel()
		importer, h := newHandler()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, importRequest(t, `{"version":`))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400 body=%q", rec.Code, rec.Body.String())
		}
		if importer.calls != 0 {
			t.Fatalf("importer calls=%d want 0", importer.calls)
		}
	})
	t.Run("get is 405", func(t *testing.T) {
		t.Parallel()
		_, h := newHandler()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/statement-observations", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d want 405", rec.Code)
		}
	})
}

// Finding 5A RED contract: the economic-detail operator route must accept an
// opaque continuation input and expose the opaque continuation output while
// remaining behind the existing protected reports mount.

type recordingEconomicDetail struct {
	detail corebilling.EconomicDetail
	err    error
	last   corebilling.EconomicDetailQuery
}

func (s *recordingEconomicDetail) QueryEconomicDetail(_ context.Context, q corebilling.EconomicDetailQuery) (corebilling.EconomicDetail, error) {
	s.last = q
	return s.detail, s.err
}

func TestOperatorEconomicDetailCursorRoundtrip(t *testing.T) {
	t.Parallel()
	reader := &recordingEconomicDetail{detail: corebilling.EconomicDetail{NextCursor: "op2.next"}}
	options := operatorTestOptions()
	options.Operator.EconomicDetail = reader
	h := NewHandler(options)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/calls/bc_1/economics?store_id=test&account_id=acct&limit=2&cursor=op2.prev", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if reader.last.Cursor != "op2.prev" {
		t.Fatalf("reader cursor=%q want op2.prev", reader.last.Cursor)
	}
	if reader.last.Limit != 2 {
		t.Fatalf("reader limit=%d want 2", reader.last.Limit)
	}
	var payload struct {
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.NextCursor != "op2.next" {
		t.Fatalf("next_cursor=%q want op2.next", payload.NextCursor)
	}
}

func TestOperatorALegEconomicDetailCursorRoundtrip(t *testing.T) {
	t.Parallel()
	reader := &recordingEconomicDetail{}
	options := operatorTestOptions()
	options.Operator.EconomicDetail = reader
	h := NewHandler(options)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a-legs/a-1/economics?store_id=test&account_id=acct&cursor=op2.prev", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if reader.last.Cursor != "op2.prev" {
		t.Fatalf("reader cursor=%q want op2.prev", reader.last.Cursor)
	}
}

func TestOperatorEconomicDetailFinalPageHasNoCursor(t *testing.T) {
	t.Parallel()
	reader := &recordingEconomicDetail{}
	options := operatorTestOptions()
	options.Operator.EconomicDetail = reader
	h := NewHandler(options)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/calls/bc_1/economics?store_id=test&account_id=acct", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["next_cursor"]; ok {
		t.Fatalf("final page must not expose next_cursor: %v", payload)
	}
}

// Finding 7 RED contract: a continuation whose frozen snapshot changed is
// rejected with the bounded, storage-neutral stale-cursor classification so the
// operator restarts pagination instead of consuming a mixed snapshot.
func TestOperatorEconomicDetailStaleCursorClassification(t *testing.T) {
	t.Parallel()
	reader := &recordingEconomicDetail{err: economics.ErrOperatorCursorStale}
	options := operatorTestOptions()
	options.Operator.EconomicDetail = reader
	h := NewHandler(options)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/calls/bc_1/economics?store_id=test&account_id=acct&cursor=op2.stale", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%q want %d", rec.Code, rec.Body.String(), http.StatusConflict)
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error != "stale_cursor" {
		t.Fatalf("error=%q want stale_cursor", payload.Error)
	}
}
