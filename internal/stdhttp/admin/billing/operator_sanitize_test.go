package billing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Task 16.3A operator non-disclosure: secret-bearing economic evidence is
// rejected at ingress, and no handler error surface echoes secret bytes.
// Responses carry only fixed codes and sanitized DTOs.

const sanitizeProbeSecret = "sk-ant-secret-probe-value"

// sanitizeNumericProbe is a non-secret single-token word that the pre-Finding-7
// suffix classifier accepted at numeric allowlist paths.
const sanitizeNumericProbe = "PrivateCustomerOutputWithoutSpaces"

func sanitizeSecretBatch(t *testing.T) string {
	t.Helper()
	return sanitizeEvidenceBatch(t, sanitizeProbeSecret)
}

func sanitizeEvidenceBatch(t *testing.T, lexeme string) string {
	t.Helper()
	subject := map[string]any{
		"kind": "statement_line", "store_id": "test", "tenant_id": "tenant-1",
		"provider_account_key": "provider-account",
		"statement_id":         "s", "statement_line_id": "line-1", "period_id": "2026-09",
	}
	batch := map[string]any{
		"version": 1, "provider_account_key": "provider-account", "statement_id": "s",
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
			"evidence": []any{map[string]any{
				"path": "$.usage.input_tokens", "lexeme": lexeme,
				"present": true, "acquisition": "statement_importer",
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
	payload, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func TestStatementImportRejectsSecretEvidenceWithoutDisclosure(t *testing.T) {
	t.Parallel()
	importer := &countingImporter{}
	auth := &recordingAuthorizer{}
	options := operatorTestOptions()
	options.Operator.StatementImport = &StatementImportOptions{Importer: importer, Authorize: auth.authorize, MaxBodyBytes: 1 << 20}
	h := NewHandler(options)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, importRequest(t, sanitizeSecretBatch(t)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 for secret-bearing evidence", rec.Code)
	}
	if strings.Contains(rec.Body.String(), sanitizeProbeSecret) {
		t.Fatal("rejection response discloses secret bytes")
	}
	if importer.calls != 0 {
		t.Fatalf("importer calls=%d want 0 for rejected evidence", importer.calls)
	}
}

func TestStatementImportRejectsNonNumericEvidenceWithoutDisclosure(t *testing.T) {
	t.Parallel()
	importer := &countingImporter{}
	auth := &recordingAuthorizer{}
	options := operatorTestOptions()
	options.Operator.StatementImport = &StatementImportOptions{Importer: importer, Authorize: auth.authorize, MaxBodyBytes: 1 << 20}
	h := NewHandler(options)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, importRequest(t, sanitizeEvidenceBatch(t, sanitizeNumericProbe)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 for non-numeric evidence at a numeric path", rec.Code)
	}
	if strings.Contains(rec.Body.String(), sanitizeNumericProbe) {
		t.Fatal("rejection response discloses the rejected lexeme")
	}
	if importer.calls != 0 {
		t.Fatalf("importer calls=%d want 0 (reject before durable write)", importer.calls)
	}
}

func TestOperatorErrorSurfaceDisclosesNothing(t *testing.T) {
	t.Parallel()
	h := NewHandler(Options{
		Queries: &recordingQueries{},
		Operator: OperatorReports{
			EconomicDetail: stubEconomicDetail{err: fmt.Errorf("%w: %s", corebilling.ErrEconomicDetailScopeMismatch, sanitizeProbeSecret)},
		},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/calls/bc_1/economics?store_id=test&account_id=acct", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), sanitizeProbeSecret) {
		t.Fatal("error response discloses secret bytes")
	}
	assertJSONError(t, rec, "not_found")
}
