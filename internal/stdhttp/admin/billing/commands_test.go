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

type recordingProvisioner struct {
	created  []corebilling.Account
	funding  []corebilling.FundingInput
	policies []corebilling.CreditPolicyInput

	createErr error
	fundErr   error
	policyErr error

	posting corebilling.Posting
	change  corebilling.PolicyChange
}

func (p *recordingProvisioner) CreateAccount(_ context.Context, account corebilling.Account) error {
	if p.createErr != nil {
		return p.createErr
	}
	if err := account.Validate(); err != nil {
		return err
	}
	p.created = append(p.created, account)
	return nil
}

func (p *recordingProvisioner) PostFunding(_ context.Context, input corebilling.FundingInput) (corebilling.Posting, error) {
	if p.fundErr != nil {
		return corebilling.Posting{}, p.fundErr
	}
	if err := input.Validate(); err != nil {
		return corebilling.Posting{}, err
	}
	p.funding = append(p.funding, input)
	return p.posting, nil
}

func (p *recordingProvisioner) ChangeCreditPolicy(_ context.Context, input corebilling.CreditPolicyInput) (corebilling.PolicyChange, error) {
	if p.policyErr != nil {
		return corebilling.PolicyChange{}, p.policyErr
	}
	if err := input.Validate(); err != nil {
		return corebilling.PolicyChange{}, err
	}
	p.policies = append(p.policies, input)
	return p.change, nil
}

var _ corebilling.AccountProvisioner = (*recordingProvisioner)(nil)

func TestBillingCommandsCreatePrepaidAccount(t *testing.T) {
	t.Parallel()
	provisioner := &recordingProvisioner{}
	h := NewHandler(Options{Commands: provisioner})
	rec := postJSON(h, "/account", `{"account_id":"acct-prepaid","currency":"USD","mode":"prepaid"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["account_id"] != "acct-prepaid" {
		t.Fatalf("payload=%v", payload)
	}
	if len(provisioner.created) != 1 {
		t.Fatalf("created=%d want 1", len(provisioner.created))
	}
	got := provisioner.created[0]
	if got.ID != "acct-prepaid" || got.Currency != "USD" || got.Mode != corebilling.AccountPrepaid {
		t.Fatalf("account=%+v", got)
	}
	if got.BalanceNano != 0 || got.CreditLimit != 0 {
		t.Fatalf("opening money fields=%+v, want zero balance and credit limit", got)
	}
	if got.State != corebilling.AccountReady {
		t.Fatalf("state=%q want ready", got.State)
	}
}

func TestBillingCommandsCreatePostpaidRequiresCreditLimit(t *testing.T) {
	t.Parallel()

	t.Run("missing credit limit", func(t *testing.T) {
		t.Parallel()
		provisioner := &recordingProvisioner{}
		rec := postJSON(NewHandler(Options{Commands: provisioner}), "/account", `{"account_id":"acct-post","currency":"USD","mode":"postpaid"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
		}
		assertJSONError(t, rec, "invalid_command")
		if len(provisioner.created) != 0 {
			t.Fatalf("created=%d want 0", len(provisioner.created))
		}
	})
	t.Run("valid postpaid", func(t *testing.T) {
		t.Parallel()
		local := &recordingProvisioner{}
		handler := NewHandler(Options{Commands: local})
		rec := postJSON(handler, "/account", `{"account_id":"acct-post","currency":"USD","mode":"postpaid","credit_limit_nano":500}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
		}
		if len(local.created) != 1 || local.created[0].CreditLimit != 500 || local.created[0].Mode != corebilling.AccountPostpaid {
			t.Fatalf("created=%+v", local.created)
		}
		if local.created[0].BalanceNano != 0 {
			t.Fatalf("opening balance=%d want 0", local.created[0].BalanceNano)
		}
	})
}

func TestBillingCommandsRejectPrepaidNonZeroCreditLimit(t *testing.T) {
	t.Parallel()
	h := NewHandler(Options{Commands: &recordingProvisioner{}})
	rec := postJSON(h, "/account", `{"account_id":"acct-prepaid","currency":"USD","mode":"prepaid","credit_limit_nano":10}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	assertJSONError(t, rec, "invalid_command")
}

func TestBillingCommandsIgnoreBalanceAndSettlementFieldsOnCreate(t *testing.T) {
	t.Parallel()
	provisioner := &recordingProvisioner{}
	h := NewHandler(Options{Commands: provisioner})
	rec := postJSON(h, "/account", `{
		"account_id":"acct-prepaid",
		"currency":"USD",
		"mode":"prepaid",
		"balance_nano":999,
		"payment_id":"pay-1",
		"invoice_id":"inv-1",
		"vat_rate":0.2,
		"fx_rate":1.1
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	if len(provisioner.created) != 1 || provisioner.created[0].BalanceNano != 0 {
		t.Fatalf("created=%+v, opening balance must stay 0", provisioner.created)
	}
}

func TestBillingCommandsCreateConflictAndInvalidJSON(t *testing.T) {
	t.Parallel()
	t.Run("identity conflict", func(t *testing.T) {
		t.Parallel()
		h := NewHandler(Options{Commands: &recordingProvisioner{createErr: corebilling.ErrAccountConflict}})
		rec := postJSON(h, "/account", `{"account_id":"acct","currency":"USD","mode":"prepaid"}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status=%d want 409 body=%s", rec.Code, rec.Body.String())
		}
		assertJSONError(t, rec, "conflict")
	})
	t.Run("invalid json", func(t *testing.T) {
		t.Parallel()
		h := NewHandler(Options{Commands: &recordingProvisioner{}})
		rec := postJSON(h, "/account", `{`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
		}
		assertJSONError(t, rec, "invalid_command")
	})
	t.Run("missing account id", func(t *testing.T) {
		t.Parallel()
		h := NewHandler(Options{Commands: &recordingProvisioner{}})
		rec := postJSON(h, "/account", `{"currency":"USD","mode":"prepaid"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
		}
		assertJSONError(t, rec, "invalid_command")
	})
}

func TestBillingCommandsNilProvisionerReturns503WhileGETReportsWork(t *testing.T) {
	t.Parallel()
	q := &recordingQueries{account: corebilling.AccountReport{Account: corebilling.Account{ID: "acct"}}}
	h := NewHandler(Options{Queries: q})
	rec := postJSON(h, "/account", `{"account_id":"acct","currency":"USD","mode":"prepaid"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("create status=%d want 503 body=%s", rec.Code, rec.Body.String())
	}
	assertJSONError(t, rec, "provisioner_unavailable")

	fundRec := postJSON(h, "/funding", `{"account_id":"acct","amount_nano":1,"currency":"USD","source_key":"s","reason":"top-up"}`)
	if fundRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("funding status=%d want 503", fundRec.Code)
	}
	policyRec := postJSON(h, "/credit-policy", `{"account_id":"acct","mode":"postpaid","currency":"USD","credit_limit_nano":1,"source_key":"s","reason":"limit"}`)
	if policyRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("policy status=%d want 503", policyRec.Code)
	}

	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/account?account_id=acct", nil))
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status=%d want 200 body=%s", getRec.Code, getRec.Body.String())
	}
	if q.lastAccountID != "acct" {
		t.Fatalf("GET account_id=%q", q.lastAccountID)
	}
}

func TestBillingCommandsPostFunding(t *testing.T) {
	t.Parallel()
	provisioner := &recordingProvisioner{
		posting: corebilling.Posting{
			OperationKey: "funding:v1:9:acct-fund:6:bank-1",
			After:        corebilling.AccountSnapshot{BalanceNano: 25, SpendableNano: 25, Currency: "USD", Mode: corebilling.AccountPrepaid},
		},
	}
	h := NewHandler(Options{Commands: provisioner})
	rec := postJSON(h, "/funding", `{"account_id":"acct-fund","amount_nano":25,"currency":"USD","source_key":"bank-1","reason":"opening top-up"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var payload corebilling.Posting
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.After.BalanceNano != 25 || payload.OperationKey != provisioner.posting.OperationKey {
		t.Fatalf("payload=%+v", payload)
	}
	if len(provisioner.funding) != 1 {
		t.Fatalf("funding calls=%d", len(provisioner.funding))
	}
	got := provisioner.funding[0]
	if got.AccountID != "acct-fund" || got.Amount.Nano != 25 || got.Amount.Currency != "USD" || got.SourceKey != "bank-1" || got.Reason != "opening top-up" {
		t.Fatalf("funding input=%+v", got)
	}
}

func TestBillingCommandsPostCreditPolicy(t *testing.T) {
	t.Parallel()
	provisioner := &recordingProvisioner{
		change: corebilling.PolicyChange{
			OperationKey: "credit_policy:v1:10:acct-policy:8:policy-1",
			After:        corebilling.AccountSnapshot{Mode: corebilling.AccountPostpaid, CreditLimitNano: 100, Currency: "USD"},
		},
	}
	h := NewHandler(Options{Commands: provisioner})
	rec := postJSON(h, "/credit-policy", `{"account_id":"acct-policy","mode":"postpaid","currency":"USD","credit_limit_nano":100,"source_key":"policy-1","reason":"enable credit"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var payload corebilling.PolicyChange
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.After.Mode != corebilling.AccountPostpaid || payload.After.CreditLimitNano != 100 {
		t.Fatalf("payload=%+v", payload)
	}
	if len(provisioner.policies) != 1 {
		t.Fatalf("policy calls=%d", len(provisioner.policies))
	}
	got := provisioner.policies[0]
	if got.AccountID != "acct-policy" || got.Mode != corebilling.AccountPostpaid || got.CreditLimit != 100 || got.SourceKey != "policy-1" || got.Reason != "enable credit" {
		t.Fatalf("policy input=%+v", got)
	}
}

func TestBillingCommandsMapDomainErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		path   string
		body   string
		setup  func(*recordingProvisioner)
		status int
		code   string
	}{
		{
			name:   "funding invalid amount",
			path:   "/funding",
			body:   `{"account_id":"acct","amount_nano":0,"currency":"USD","source_key":"s","reason":"top-up"}`,
			status: http.StatusBadRequest,
			code:   "invalid_command",
		},
		{
			name: "funding missing account",
			path: "/funding",
			body: `{"account_id":"missing","amount_nano":1,"currency":"USD","source_key":"s","reason":"top-up"}`,
			setup: func(p *recordingProvisioner) {
				p.fundErr = corebilling.ErrAccountNotFound
			},
			status: http.StatusNotFound,
			code:   "not_found",
		},
		{
			name: "funding conflict",
			path: "/funding",
			body: `{"account_id":"acct","amount_nano":1,"currency":"USD","source_key":"s","reason":"top-up"}`,
			setup: func(p *recordingProvisioner) {
				p.fundErr = corebilling.ErrAccountConflict
			},
			status: http.StatusConflict,
			code:   "conflict",
		},
		{
			name:   "policy prepaid non-zero limit",
			path:   "/credit-policy",
			body:   `{"account_id":"acct","mode":"prepaid","currency":"USD","credit_limit_nano":5,"source_key":"s","reason":"limit"}`,
			status: http.StatusBadRequest,
			code:   "invalid_command",
		},
		{
			name:   "policy missing credit limit",
			path:   "/credit-policy",
			body:   `{"account_id":"acct","mode":"postpaid","currency":"USD","source_key":"s","reason":"limit"}`,
			status: http.StatusBadRequest,
			code:   "invalid_command",
		},
		{
			name: "policy missing account",
			path: "/credit-policy",
			body: `{"account_id":"missing","mode":"postpaid","currency":"USD","credit_limit_nano":10,"source_key":"s","reason":"limit"}`,
			setup: func(p *recordingProvisioner) {
				p.policyErr = corebilling.ErrAccountNotFound
			},
			status: http.StatusNotFound,
			code:   "not_found",
		},
		{
			name: "policy conflict",
			path: "/credit-policy",
			body: `{"account_id":"acct","mode":"postpaid","currency":"USD","credit_limit_nano":10,"source_key":"s","reason":"limit"}`,
			setup: func(p *recordingProvisioner) {
				p.policyErr = corebilling.ErrAccountConflict
			},
			status: http.StatusConflict,
			code:   "conflict",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provisioner := &recordingProvisioner{}
			if tt.setup != nil {
				tt.setup(provisioner)
			}
			rec := postJSON(NewHandler(Options{Commands: provisioner}), tt.path, tt.body)
			if rec.Code != tt.status {
				t.Fatalf("status=%d want %d body=%s", rec.Code, tt.status, rec.Body.String())
			}
			assertJSONError(t, rec, tt.code)
		})
	}
}

func TestBillingCommandsCreateFundPolicyThenGETAccount(t *testing.T) {
	t.Parallel()
	provisioner := &recordingProvisioner{
		posting: corebilling.Posting{After: corebilling.AccountSnapshot{BalanceNano: 50, SpendableNano: 50, Currency: "USD", Mode: corebilling.AccountPrepaid}},
		change:  corebilling.PolicyChange{After: corebilling.AccountSnapshot{BalanceNano: 50, CreditLimitNano: 100, Currency: "USD", Mode: corebilling.AccountPostpaid}},
	}
	queries := &recordingQueries{
		account: corebilling.AccountReport{
			Account: corebilling.Account{
				ID:          "acct-loop",
				Currency:    "USD",
				Mode:        corebilling.AccountPostpaid,
				CreditLimit: 100,
				BalanceNano: 50,
				State:       corebilling.AccountReady,
				Version:     3,
			},
			SpendableNano:   150,
			CreditFloorNano: -100,
		},
	}
	h := NewHandler(Options{Queries: queries, Commands: provisioner})

	createRec := postJSON(h, "/account", `{"account_id":"acct-loop","currency":"USD","mode":"prepaid"}`)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	fundRec := postJSON(h, "/funding", `{"account_id":"acct-loop","amount_nano":50,"currency":"USD","source_key":"bank-1","reason":"opening top-up"}`)
	if fundRec.Code != http.StatusOK {
		t.Fatalf("funding status=%d body=%s", fundRec.Code, fundRec.Body.String())
	}
	policyRec := postJSON(h, "/credit-policy", `{"account_id":"acct-loop","mode":"postpaid","currency":"USD","credit_limit_nano":100,"source_key":"policy-1","reason":"enable credit"}`)
	if policyRec.Code != http.StatusOK {
		t.Fatalf("policy status=%d body=%s", policyRec.Code, policyRec.Body.String())
	}

	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/account?account_id=acct-loop", nil))
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", getRec.Code, getRec.Body.String())
	}
	if queries.lastAccountID != "acct-loop" {
		t.Fatalf("GET forwarded account_id=%q", queries.lastAccountID)
	}
	var report corebilling.AccountReport
	if err := json.Unmarshal(getRec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Account.ID != "acct-loop" || report.Account.Mode != corebilling.AccountPostpaid || report.Account.BalanceNano != 50 || report.Account.CreditLimit != 100 {
		t.Fatalf("report=%+v", report.Account)
	}
	if len(provisioner.created) != 1 || len(provisioner.funding) != 1 || len(provisioner.policies) != 1 {
		t.Fatalf("commands create=%d fund=%d policy=%d", len(provisioner.created), len(provisioner.funding), len(provisioner.policies))
	}
}

func TestBillingCommandsRejectUntrustedMethods(t *testing.T) {
	t.Parallel()
	h := NewHandler(Options{Queries: &recordingQueries{}, Commands: &recordingProvisioner{}})
	tests := []struct {
		method string
		path   string
		allow  string
	}{
		{method: http.MethodPut, path: "/account", allow: "GET, POST"},
		{method: http.MethodPatch, path: "/account", allow: "GET, POST"},
		{method: http.MethodGet, path: "/funding", allow: http.MethodPost},
		{method: http.MethodPut, path: "/funding", allow: http.MethodPost},
		{method: http.MethodGet, path: "/credit-policy", allow: http.MethodPost},
		{method: http.MethodPatch, path: "/credit-policy", allow: http.MethodPost},
		{method: http.MethodGet, path: "/exposure-repair", allow: http.MethodPost},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status=%d want 405 body=%s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Allow") != tt.allow {
				t.Fatalf("Allow=%q want %q", rec.Header().Get("Allow"), tt.allow)
			}
			assertJSONError(t, rec, "method_not_allowed")
		})
	}
}

type recordingRecovery struct {
	completeCalls   []string
	incompleteCalls []string
	sourceKeys      []string
	err             error
	settlement      corebilling.CallSettlement
}

func (r *recordingRecovery) RepairExposureNoCharge(_ context.Context, callID corebilling.BillingCallID, sourceKey string) (corebilling.CallSettlement, error) {
	r.completeCalls = append(r.completeCalls, callID.String())
	r.sourceKeys = append(r.sourceKeys, sourceKey)
	if r.err != nil {
		return corebilling.CallSettlement{}, r.err
	}
	return r.settlement, nil
}

func (r *recordingRecovery) RepairIncompleteCallNoCharge(_ context.Context, callID corebilling.BillingCallID, sourceKey string) (corebilling.CallSettlement, error) {
	r.incompleteCalls = append(r.incompleteCalls, callID.String())
	r.sourceKeys = append(r.sourceKeys, sourceKey)
	if r.err != nil {
		return corebilling.CallSettlement{}, r.err
	}
	return r.settlement, nil
}

var _ corebilling.ExposureRecovery = (*recordingRecovery)(nil)

func TestBillingExposureRepairCompleteAndIncomplete(t *testing.T) {
	t.Parallel()
	callID, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	recovery := &recordingRecovery{settlement: corebilling.CallSettlement{CallID: callID}}
	h := NewHandler(Options{Recovery: recovery})

	complete := postJSON(h, "/exposure-repair", `{"call_id":"`+callID.String()+`","source_key":"op-1","mode":"complete"}`)
	if complete.Code != http.StatusOK {
		t.Fatalf("complete status=%d body=%s", complete.Code, complete.Body.String())
	}
	incomplete := postJSON(h, "/exposure-repair", `{"call_id":"`+callID.String()+`","source_key":"op-2","mode":"incomplete"}`)
	if incomplete.Code != http.StatusOK {
		t.Fatalf("incomplete status=%d body=%s", incomplete.Code, incomplete.Body.String())
	}
	if len(recovery.completeCalls) != 1 || recovery.completeCalls[0] != callID.String() {
		t.Fatalf("complete calls=%v", recovery.completeCalls)
	}
	if len(recovery.incompleteCalls) != 1 || recovery.incompleteCalls[0] != callID.String() {
		t.Fatalf("incomplete calls=%v", recovery.incompleteCalls)
	}
}

func TestBillingExposureRepairMapsErrors(t *testing.T) {
	t.Parallel()
	callID, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("nil recovery", func(t *testing.T) {
		t.Parallel()
		rec := postJSON(NewHandler(Options{}), "/exposure-repair", `{"call_id":"`+callID.String()+`","source_key":"op","mode":"complete"}`)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d want 503 body=%s", rec.Code, rec.Body.String())
		}
		assertJSONError(t, rec, "recovery_unavailable")
	})
	t.Run("invalid mode", func(t *testing.T) {
		t.Parallel()
		rec := postJSON(NewHandler(Options{Recovery: &recordingRecovery{}}), "/exposure-repair", `{"call_id":"`+callID.String()+`","source_key":"op","mode":"force"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
		}
		assertJSONError(t, rec, "invalid_command")
	})
	t.Run("incomplete evidence", func(t *testing.T) {
		t.Parallel()
		rec := postJSON(NewHandler(Options{Recovery: &recordingRecovery{err: corebilling.ErrCallIncomplete}}), "/exposure-repair", `{"call_id":"`+callID.String()+`","source_key":"op","mode":"incomplete"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
		}
		assertJSONError(t, rec, "not_found")
	})
}

func postJSON(h http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
