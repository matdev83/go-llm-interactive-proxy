package stdhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	httpauth "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

// Task 16.2B mount contract: operator routes live under the existing
// configured/protected ReportsPath with the diagnostics secret enforced
// before any reader runs. Reports-only composition never registers the
// statement import route. Frontend inference responses carry no operator
// economics fields.

type operatorStubDiscrepancies struct{}

func (operatorStubDiscrepancies) QueryDiscrepancies(context.Context, economics.DiscrepancyQuery) (economics.DiscrepancyPage, error) {
	return economics.DiscrepancyPage{}, nil
}

type operatorStubAdjustments struct{}

func (operatorStubAdjustments) QueryAdjustments(context.Context, economics.AdjustmentQuery) (economics.AdjustmentPage, error) {
	return economics.AdjustmentPage{}, nil
}

func operatorReportsOperations(reports billing.Queries) HTTPOperationsInput {
	return HTTPOperationsInput{
		BillingReports:     reports,
		BillingReportsPath: "/admin/billing",
		BillingOperatorReports: billing.OperatorReports{
			Discrepancies: operatorStubDiscrepancies{},
			Adjustments:   operatorStubAdjustments{},
		},
	}
}

func serveOperator(t *testing.T, mux http.Handler, secret, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if secret != "" {
		req.Header.Set("X-LIP-Diagnostics-Secret", secret)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestOperatorRoutesMountedAndProtected(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	cfg := &config.Config{Diagnostics: config.DiagnosticsConfig{SharedSecret: "operator-secret"}}
	mountBillingReports(billingReportsMount{
		Mux: mux, Cfg: cfg, Operations: operatorReportsOperations(billingAccountReportStub()),
	})

	for _, path := range []string{
		"/admin/billing/reconciliations?store_id=test&tenant_id=t",
		"/admin/billing/adjustments?store_id=test&account_id=acct",
	} {
		denied := serveOperator(t, mux, "", path)
		if denied.Code != http.StatusForbidden {
			t.Fatalf("path %s without secret status=%d want 403", path, denied.Code)
		}
		allowed := serveOperator(t, mux, "operator-secret", path)
		if allowed.Code != http.StatusOK {
			t.Fatalf("path %s with secret status=%d body=%q", path, allowed.Code, allowed.Body.String())
		}
	}
}

func TestOperatorImportAbsentInReportsOnlyComposition(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	cfg := &config.Config{Diagnostics: config.DiagnosticsConfig{SharedSecret: "operator-secret"}}
	mountBillingReports(billingReportsMount{
		Mux: mux, Cfg: cfg, Operations: operatorReportsOperations(billingAccountReportStub()),
	})

	req := httptest.NewRequest(http.MethodPost, "/admin/billing/statement-observations", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LIP-Diagnostics-Secret", "operator-secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("import route without opt-in status=%d want 404", rec.Code)
	}
}

func TestOperatorRoutesDisabledWithoutReaders(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	cfg := &config.Config{Diagnostics: config.DiagnosticsConfig{SharedSecret: "operator-secret"}}
	mountBillingReports(billingReportsMount{
		Mux: mux, Cfg: cfg, Operations: HTTPOperationsInput{
			BillingReports:     billingAccountReportStub(),
			BillingReportsPath: "/admin/billing",
		},
	})

	rec := serveOperator(t, mux, "operator-secret", "/admin/billing/reconciliations?store_id=test&tenant_id=t")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 disabled", rec.Code)
	}

	existing := serveOperator(t, mux, "operator-secret", "/admin/billing/account?account_id=acct&limit=10")
	if existing.Code != http.StatusOK {
		t.Fatalf("existing account route status=%d want 200", existing.Code)
	}
}

func TestFrontendResponsesContainNoOperatorEconomics(t *testing.T) {
	t.Parallel()
	reg := isolationRegistry(t)
	var seen sync.Map
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "ok", &seen)
	mux := http.NewServeMux()
	plugins := []config.PluginConfig{{ID: "openai-legacy", Enabled: true}}
	if err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux: mux,
		Frontends: HTTPFrontendInput{
			Executor: ex, DefaultRouteSelector: "stub:x", Plugins: plugins, Registry: reg,
		},
	}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"stub:x","messages":[{"role":"user","content":"ping"}]}`))
	r = r.WithContext(httpauth.WithPrincipal(r.Context(), execview.PrincipalView{ID: "isolation-test"}))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"valuation", "discrepanc", "allowance", "adjustment", "statement_line",
		"selected_cost", "provider_cost", "gross_margin", "reconciliation", "billing",
	}
	var walk func(value any, path string)
	walk = func(value any, path string) {
		switch node := value.(type) {
		case map[string]any:
			for key, child := range node {
				lower := strings.ToLower(key)
				for _, word := range forbidden {
					if strings.Contains(lower, word) {
						t.Fatalf("frontend response field %s contains operator economics %q", path+"/"+key, word)
					}
				}
				walk(child, path+"/"+key)
			}
		case []any:
			for _, child := range node {
				walk(child, path+"/[]")
			}
		}
	}
	walk(out, "")
}
