package billing

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	_ "modernc.org/sqlite"
)

func observationFingerprint(t *testing.T, observation map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	var decoded metering.Observation
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.Fingerprint()
}

// Task 16.2B import integration: a real billingstore ledger behind the real
// scope-bound importer serves the HTTP import route. The first POST accepts
// the matched line and reports the aggregate line unmatched; the identical
// replay reports both lines replayed with no duplicate effects and no model
// call (the handler owns no executor).

var importE2ESequence atomic.Int64

func TestStatementImportEndToEndAcceptedReplayUnmatched(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sqlDB, err := sql.Open("sqlite", "file:statement-import-e2e-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	sqlDB.SetMaxOpenConns(4)
	bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := billingstore.NewDurableStore(ctx, bunDB, billingstore.Config{StoreID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	service, err := corebilling.NewStatementImportService(store)
	if err != nil {
		t.Fatal(err)
	}
	importer, err := corebilling.NewBoundStatementImporter(service, corebilling.TrustedStatementScope{
		StoreID: "test", TenantID: "tenant-1", PrincipalID: "principal-1",
		ProviderAccountKeys: []string{"provider-account"},
	})
	if err != nil {
		t.Fatal(err)
	}
	auth := &recordingAuthorizer{}
	h := NewHandler(Options{Operator: OperatorReports{
		StatementImport: &StatementImportOptions{Importer: importer, Authorize: auth.authorize},
	}})

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/statement-observations", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	batch := importTestBatchTwoLines(t)
	first := post(batch)
	if first.Code != http.StatusOK {
		t.Fatalf("first import status=%d body=%q", first.Code, first.Body.String())
	}
	var accepted struct {
		Accepted  []string `json:"accepted"`
		Replayed  []string `json:"replayed"`
		Unmatched []string `json:"unmatched"`
		Rejected  []string `json:"rejected"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if len(accepted.Accepted) != 1 || accepted.Accepted[0] != "line-1" {
		t.Fatalf("accepted=%+v want [line-1]", accepted)
	}
	if len(accepted.Unmatched) != 1 || accepted.Unmatched[0] != "line-2" {
		t.Fatalf("unmatched=%+v want [line-2]", accepted)
	}
	if len(accepted.Rejected) != 0 {
		t.Fatalf("rejected=%+v want empty", accepted)
	}

	replay := post(batch)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%q", replay.Code, replay.Body.String())
	}
	var replayed struct {
		Replayed []string `json:"replayed"`
	}
	if err := json.Unmarshal(replay.Body.Bytes(), &replayed); err != nil {
		t.Fatal(err)
	}
	if len(replayed.Replayed) != 2 {
		t.Fatalf("replayed=%+v want both lines without duplicate effects", replayed)
	}
	if auth.calls != 2 {
		t.Fatalf("authorizer calls=%d want 2", auth.calls)
	}
	_ = importE2ESequence.Add(1)
}

func importTestBatchTwoLines(t *testing.T) string {
	t.Helper()
	subject := func(line string) map[string]any {
		return map[string]any{
			"kind": "statement_line", "store_id": "test", "tenant_id": "tenant-1",
			"provider_account_key": "provider-account",
			"statement_id":         "s", "statement_line_id": line, "period_id": "2026-09",
		}
	}
	observation := map[string]any{
		"version": 2, "id": "obs-1", "source_event_key": "event-1", "revision": 1,
		"stream_id": "stream-1", "sequence": 1,
		"origin": "statement", "acquisition": "statement_importer", "authority": "verified_statement",
		"perspective": "operator", "boundary": "backend_ingress", "lifecycle": "backend_attempt",
		"subject": subject("line-1"), "correlation": map[string]any{"store_id": "test"},
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
	}
	batch := map[string]any{
		"version": 1, "provider_account_key": "provider-account", "statement_id": "s",
		"revision": 1, "period_id": "2026-09", "subject": subject("line-1"),
		"observations": []any{observation},
		"lines": []any{
			map[string]any{
				"id": "line-1", "revision": 1, "subject": subject("line-1"),
				"observation": map[string]any{
					"store_id": "test", "observation_id": "obs-1", "revision": 1,
					"payload_hash": observationFingerprint(t, observation),
				},
				"charge_item_id": "charge-1",
			},
			map[string]any{
				"id": "line-2", "revision": 1, "subject": subject("line-2"),
				"outcome": "unmatched", "unmatched_reason": "account-period aggregate",
			},
		},
	}
	payload, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}
