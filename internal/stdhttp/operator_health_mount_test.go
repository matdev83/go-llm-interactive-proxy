package stdhttp

import (
	"context"
	"net/http"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	billingadmin "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/billing"
)

// Task 16.3B mount contract: the economics health route lives under the
// existing protected reports mount. The diagnostics secret is enforced
// before any snapshot read; reports-only composition without a health
// reader reports disabled.

type mountStubHealth struct{}

func (mountStubHealth) EconomicHealthSnapshot(context.Context) (billing.EconomicHealthSnapshot, error) {
	return billing.EconomicHealthSnapshot{}, nil
}

func TestOperatorHealthMountedAndProtected(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	cfg := &config.Config{Diagnostics: config.DiagnosticsConfig{SharedSecret: "health-secret"}}
	mountBillingReports(billingReportsMount{
		Mux: mux, Cfg: cfg, Operations: HTTPOperationsInput{
			BillingReports:     billingAccountReportStub(),
			BillingReportsPath: "/admin/billing",
			BillingOperatorReports: billingadmin.OperatorReports{
				Health: mountStubHealth{},
			},
		},
	})

	denied := serveOperator(t, mux, "", "/admin/billing/health")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("health without secret status=%d want 403", denied.Code)
	}
	allowed := serveOperator(t, mux, "health-secret", "/admin/billing/health")
	if allowed.Code != http.StatusOK {
		t.Fatalf("health with secret status=%d body=%q", allowed.Code, allowed.Body.String())
	}
}

func TestOperatorHealthDisabledWithoutReader(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	cfg := &config.Config{Diagnostics: config.DiagnosticsConfig{SharedSecret: "health-secret"}}
	mountBillingReports(billingReportsMount{
		Mux: mux, Cfg: cfg, Operations: HTTPOperationsInput{
			BillingReports:     billingAccountReportStub(),
			BillingReportsPath: "/admin/billing",
		},
	})
	rec := serveOperator(t, mux, "health-secret", "/admin/billing/health")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("health without reader status=%d want 404 disabled", rec.Code)
	}
}
