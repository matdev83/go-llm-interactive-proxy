package billing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Task 16.3B RED contract: protected economics health route. The snapshot
// carries bounded counts/ages/totals and safe enums only, never
// per-request/account identifiers or raw content.

type stubHealth struct {
	snapshot corebilling.EconomicHealthSnapshot
	err      error
}

func (s stubHealth) EconomicHealthSnapshot(context.Context) (corebilling.EconomicHealthSnapshot, error) {
	return s.snapshot, s.err
}

func TestOperatorHealthRoute(t *testing.T) {
	t.Parallel()
	h := NewHandler(Options{
		Queries: &recordingQueries{},
		Operator: OperatorReports{
			Health: stubHealth{snapshot: corebilling.EconomicHealthSnapshot{
				Queues: []corebilling.EconomicQueueHealth{{Queue: "customer", Pending: 2}},
			}},
		},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["queues"]; !ok {
		t.Fatalf("health payload lacks queues: %v", payload)
	}
	raw := strings.ToLower(rec.Body.String())
	for _, forbidden := range []string{"bc_", "call_id", "b-leg", "tenant", "secret", "password"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("health payload leaks identifier %q: %s", forbidden, rec.Body.String())
		}
	}
}

func TestOperatorHealthDisabledWithoutReader(t *testing.T) {
	t.Parallel()
	h := NewHandler(Options{Queries: &recordingQueries{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 disabled", rec.Code)
	}
	assertJSONError(t, rec, "disabled")
}

func TestOperatorHealthMethodNotAllowed(t *testing.T) {
	t.Parallel()
	h := NewHandler(Options{Queries: &recordingQueries{}, Operator: OperatorReports{Health: stubHealth{}}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}
