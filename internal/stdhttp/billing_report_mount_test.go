package stdhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

type billingReportQueriesStub struct {
	account billing.AccountReport
	turn    billing.TurnExplanation
	session billing.SessionReport
	err     error
}

func (s billingReportQueriesStub) AccountReport(context.Context, string, billing.PageRequest) (billing.AccountReport, error) {
	return s.account, s.err
}

func (s billingReportQueriesStub) TurnExplanation(context.Context, string) (billing.TurnExplanation, error) {
	return s.turn, s.err
}

func (billingReportQueriesStub) OperatorCostReport(context.Context, billing.ReportFilter) (billing.OperatorCostReport, error) {
	return billing.OperatorCostReport{}, nil
}

func (billingReportQueriesStub) TrialBalanceReport(context.Context, billing.ReportFilter) (billing.TrialBalanceReport, error) {
	return billing.TrialBalanceReport{AccountID: "acct", PageBalanced: true, Balanced: true}, nil
}

func (billingReportQueriesStub) QueryProcessing(context.Context, billing.ReportFilter) (billing.ProcessingPage, error) {
	return billing.ProcessingPage{}, nil
}

func (billingReportQueriesStub) QueryOpenHolds(context.Context, string, billing.PageRequest) (billing.HoldPage, error) {
	return billing.HoldPage{}, nil
}

func (billingReportQueriesStub) QueryReconcileRequired(context.Context, billing.PageRequest) (billing.AccountStatePage, error) {
	return billing.AccountStatePage{}, nil
}

func (s billingReportQueriesStub) SessionReport(context.Context, string, string, billing.PageRequest) (billing.SessionReport, error) {
	return s.session, s.err
}

type billingProvisionerStub struct {
	created []billing.Account
}

func (s *billingProvisionerStub) CreateAccount(_ context.Context, account billing.Account) error {
	s.created = append(s.created, account)
	return nil
}

func (s *billingProvisionerStub) PostFunding(context.Context, billing.FundingInput) (billing.Posting, error) {
	return billing.Posting{}, nil
}

func (s *billingProvisionerStub) ChangeCreditPolicy(context.Context, billing.CreditPolicyInput) (billing.PolicyChange, error) {
	return billing.PolicyChange{}, nil
}

var _ billing.AccountProvisioner = (*billingProvisionerStub)(nil)

const billingCreateAccountJSON = `{"account_id":"acct","currency":"USD","mode":"prepaid"}`

func billingAccountReportStub() billingReportQueriesStub {
	return billingReportQueriesStub{
		account: billing.AccountReport{
			Account:         billing.Account{ID: "acct", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 92, ReservedNano: 0, State: billing.AccountReady},
			SpendableNano:   92,
			CreditFloorNano: 0,
		},
	}
}

func postBillingAccount(mux http.Handler, secret string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/admin/billing/account", strings.NewReader(billingCreateAccountJSON))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-LIP-Diagnostics-Secret", secret)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestBillingReportsMountedAndProtected(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	cfg := &config.Config{Diagnostics: config.DiagnosticsConfig{SharedSecret: "billing-secret"}}
	provisioner := &billingProvisionerStub{}
	mountBillingReports(billingReportsMount{
		Mux: mux, Cfg: cfg, Operations: HTTPOperationsInput{
			BillingReports:     billingAccountReportStub(),
			BillingReportsPath: "/admin/billing",
			BillingProvisioner: provisioner,
		},
	})

	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/admin/billing/trial-balance?account_id=acct", nil))
	if missing.Code != http.StatusForbidden {
		t.Fatalf("missing secret status=%d want=%d", missing.Code, http.StatusForbidden)
	}

	missingPOST := postBillingAccount(mux, "")
	if missingPOST.Code != http.StatusForbidden {
		t.Fatalf("missing secret POST status=%d want=%d body=%q", missingPOST.Code, http.StatusForbidden, missingPOST.Body.String())
	}
	if len(provisioner.created) != 0 {
		t.Fatalf("provisioner called without diagnostics secret: %+v", provisioner.created)
	}

	allowed := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/billing/account?account_id=acct&limit=10", nil)
	req.Header.Set("X-LIP-Diagnostics-Secret", "billing-secret")
	mux.ServeHTTP(allowed, req)
	if allowed.Code != http.StatusOK || allowed.Body.Len() == 0 {
		t.Fatalf("allowed response status=%d body=%q", allowed.Code, allowed.Body.String())
	}
	var payload billing.AccountReport
	if err := json.Unmarshal(allowed.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Account.ID != "acct" || payload.SpendableNano != 92 || payload.CreditFloorNano != 0 {
		t.Fatalf("account payload missing spendable projection: %+v", payload)
	}

	created := postBillingAccount(mux, "billing-secret")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d want=%d body=%q", created.Code, http.StatusCreated, created.Body.String())
	}
	if len(provisioner.created) != 1 || provisioner.created[0].ID != "acct" {
		t.Fatalf("created=%+v", provisioner.created)
	}
}

func TestBillingReportsEmptySecretDoesNotMountProvisioning(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	cfg := &config.Config{Diagnostics: config.DiagnosticsConfig{SharedSecret: ""}}
	provisioner := &billingProvisionerStub{}
	mountBillingReports(billingReportsMount{
		Mux: mux, Cfg: cfg, Operations: HTTPOperationsInput{
			BillingReports:     billingAccountReportStub(),
			BillingReportsPath: "/admin/billing",
			BillingProvisioner: provisioner,
		},
	})

	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/admin/billing/account?account_id=acct&limit=10", nil))
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("empty secret GET status=%d want=%d body=%q", getRec.Code, http.StatusNotFound, getRec.Body.String())
	}

	postRec := postBillingAccount(mux, "")
	if postRec.Code != http.StatusNotFound {
		t.Fatalf("empty secret POST status=%d want=%d body=%q", postRec.Code, http.StatusNotFound, postRec.Body.String())
	}
	if len(provisioner.created) != 0 {
		t.Fatalf("empty secret served provisioning: %+v", provisioner.created)
	}
}

func TestBillingReportsProvisioningNotOnFrontendRoutes(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	cfg := &config.Config{Diagnostics: config.DiagnosticsConfig{SharedSecret: "billing-secret"}}
	provisioner := &billingProvisionerStub{}
	mountBillingReports(billingReportsMount{
		Mux: mux, Cfg: cfg, Operations: HTTPOperationsInput{
			BillingReports:     billingAccountReportStub(),
			BillingReportsPath: "/admin/billing",
			BillingProvisioner: provisioner,
		},
	})
	frontendHits := 0
	mux.HandleFunc("/v1/responses", func(w http.ResponseWriter, _ *http.Request) {
		frontendHits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"output":"ok"}`))
	})

	for _, path := range []string{"/v1/responses", "/v1/account", "/v1/chat/completions"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(billingCreateAccountJSON))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-LIP-Diagnostics-Secret", "billing-secret")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if path == "/v1/responses" {
			if rec.Code != http.StatusOK {
				t.Fatalf("%s status=%d want 200 body=%q", path, rec.Code, rec.Body.String())
			}
			continue
		}
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d want 404 (must not be a billing command) body=%q", path, rec.Code, rec.Body.String())
		}
	}
	if frontendHits != 1 {
		t.Fatalf("frontend hits=%d want 1", frontendHits)
	}
	if len(provisioner.created) != 0 {
		t.Fatalf("client frontend invoked provisioner: %+v", provisioner.created)
	}
}

func TestBillingReportsMountContractIncludesProvisioner(t *testing.T) {
	t.Parallel()
	for _, row := range mountContractInventory {
		if row.Helper != "mountBillingReports" {
			continue
		}
		if slices.Contains(row.BuiltFields, "BillingProvisioner") {
			return
		}
		t.Fatalf("mountBillingReports BuiltFields=%v missing BillingProvisioner", row.BuiltFields)
	}
	t.Fatal("mountBillingReports inventory row missing")
}

func TestBillingReportsMapDomainErrors(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	cfg := &config.Config{Diagnostics: config.DiagnosticsConfig{SharedSecret: "billing-secret"}}
	mountBillingReports(billingReportsMount{
		Mux: mux, Cfg: cfg, Operations: HTTPOperationsInput{
			BillingReports:     billingReportQueriesStub{err: billing.ErrReportNotFound},
			BillingReportsPath: "/admin/billing",
		},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/billing/turn?tur_key=missing", nil)
	req.Header.Set("X-LIP-Diagnostics-Secret", "billing-secret")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("not found status=%d body=%q", rec.Code, rec.Body.String())
	}

	mux = http.NewServeMux()
	mountBillingReports(billingReportsMount{
		Mux: mux, Cfg: cfg, Operations: HTTPOperationsInput{
			BillingReports:     billingReportQueriesStub{err: billing.ErrReportInvalid},
			BillingReportsPath: "/admin/billing",
		},
	})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/admin/billing/account?account_id=acct&limit=10", nil)
	req.Header.Set("X-LIP-Diagnostics-Secret", "billing-secret")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestBillingReportsRejectInvalidStatusAndTime(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	cfg := &config.Config{Diagnostics: config.DiagnosticsConfig{SharedSecret: "billing-secret"}}
	mountBillingReports(billingReportsMount{
		Mux: mux, Cfg: cfg, Operations: HTTPOperationsInput{
			BillingReports: billingReportQueriesStub{}, BillingReportsPath: "/admin/billing",
		},
	})
	for _, path := range []string{
		"/admin/billing/processing?status=not-a-status",
		"/admin/billing/operator-cost?account_id=acct&from=yesterday",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-LIP-Diagnostics-Secret", "billing-secret")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d want 400", path, rec.Code)
		}
	}
}

func TestBillingSessionReportRequiresAccountAndSession(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	cfg := &config.Config{Diagnostics: config.DiagnosticsConfig{SharedSecret: "billing-secret"}}
	mountBillingReports(billingReportsMount{
		Mux: mux, Cfg: cfg, Operations: HTTPOperationsInput{
			BillingReports: billingReportQueriesStub{
				session: billing.SessionReport{AccountID: "acct", SessionID: "sess", CustomerRevenue: billing.Money{Nano: 13, Currency: "USD"}},
			},
			BillingReportsPath: "/admin/billing",
		},
	})
	for _, path := range []string{
		"/admin/billing/session?account_id=acct",
		"/admin/billing/session?session_id=sess",
		"/admin/billing/session",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-LIP-Diagnostics-Secret", "billing-secret")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d want 400", path, rec.Code)
		}
	}

	ok := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/billing/session?account_id=acct&session_id=sess", nil)
	req.Header.Set("X-LIP-Diagnostics-Secret", "billing-secret")
	mux.ServeHTTP(ok, req)
	if ok.Code != http.StatusOK {
		t.Fatalf("valid session report status=%d body=%q", ok.Code, ok.Body.String())
	}
	var payload billing.SessionReport
	if err := json.Unmarshal(ok.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SessionID != "sess" || payload.CustomerRevenue.Nano != 13 {
		t.Fatalf("session payload = %+v", payload)
	}
}
