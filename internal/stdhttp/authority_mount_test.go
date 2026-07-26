package stdhttp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/configsource"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	cpadmin "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/controlplane"
	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type authorityPageResponse[T any] struct {
	State authorityapp.QueryState `json:"state"`
	Page  cp.Page[T]              `json:"page"`
}

func TestAccountingAuthorityQueryNotMountedWhenDisabled(t *testing.T) {
	t.Parallel()
	cfg := authorityHTTPConfig(false)
	in, _ := authorityHTTPInput(t, cfg)
	app := mustRuntimeApp(t, cfg)
	ctx := context.Background()
	startTestApp(ctx, t, app)
	h, err := ComposeStandardHTTP(ctx, cfg, slog.Default(), in)
	if err != nil {
		t.Fatalf("ComposeStandardHTTP: %v", err)
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/authority/status", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestAccountingAuthorityQueryMountedAndProtected(t *testing.T) {
	t.Parallel()
	cfg := authorityHTTPConfig(true)
	in, svc := authorityHTTPInput(t, cfg)
	seedAuthorityDecisionHistory(t, svc)
	app := mustRuntimeApp(t, cfg)
	ctx := context.Background()
	startTestApp(ctx, t, app)
	h, err := ComposeStandardHTTP(ctx, cfg, slog.Default(), in)
	if err != nil {
		t.Fatalf("ComposeStandardHTTP: %v", err)
	}

	missing := httptest.NewRecorder()
	h.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/authority/status", nil))
	if missing.Code != http.StatusForbidden {
		t.Fatalf("missing secret status = %d body=%s", missing.Code, missing.Body.String())
	}

	okReq := httptest.NewRequest(http.MethodGet, "/authority/status", nil)
	okReq.Header.Set(diag.HeaderDiagnosticsSecret, cfg.Diagnostics.SharedSecret)
	ok := httptest.NewRecorder()
	h.ServeHTTP(ok, okReq)
	if ok.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", ok.Code, ok.Body.String())
	}
	if !strings.Contains(ok.Body.String(), `"state":"ready"`) {
		t.Fatalf("status body missing ready state: %s", ok.Body.String())
	}
	assertNoUnsafeAuthorityBody(t, ok.Body.String())

	page1Req := httptest.NewRequest(http.MethodGet, "/authority/decision-history?limit=1&rule_id=tenant.requests", nil)
	page1Req.Header.Set(diag.HeaderDiagnosticsSecret, cfg.Diagnostics.SharedSecret)
	page1 := httptest.NewRecorder()
	h.ServeHTTP(page1, page1Req)
	if page1.Code != http.StatusOK {
		t.Fatalf("page1 status = %d body=%s", page1.Code, page1.Body.String())
	}
	page1Body := decodeAuthorityPage[cp.AccountingDecisionRow](t, page1.Body.Bytes())
	if page1Body.State != authorityapp.QueryStateReady {
		t.Fatalf("page1 state = %q body=%s", page1Body.State, page1.Body.String())
	}
	if len(page1Body.Page.Items) != 1 {
		t.Fatalf("page1 items = %d body=%s", len(page1Body.Page.Items), page1.Body.String())
	}
	if page1Body.Page.Next.Token == "" {
		t.Fatalf("page1 next cursor missing: %s", page1.Body.String())
	}
	assertNoUnsafeAuthorityBody(t, page1.Body.String())

	page2Req := httptest.NewRequest(http.MethodGet, "/authority/decision-history?limit=1&rule_id=tenant.requests&cursor="+url.QueryEscape(page1Body.Page.Next.Token), nil)
	page2Req.Header.Set(diag.HeaderDiagnosticsSecret, cfg.Diagnostics.SharedSecret)
	page2 := httptest.NewRecorder()
	h.ServeHTTP(page2, page2Req)
	if page2.Code != http.StatusOK {
		t.Fatalf("page2 status = %d body=%s", page2.Code, page2.Body.String())
	}
	page2Body := decodeAuthorityPage[cp.AccountingDecisionRow](t, page2.Body.Bytes())
	if page2Body.State != authorityapp.QueryStateReady {
		t.Fatalf("page2 state = %q body=%s", page2Body.State, page2.Body.String())
	}
	if len(page2Body.Page.Items) != 1 {
		t.Fatalf("page2 items = %d body=%s", len(page2Body.Page.Items), page2.Body.String())
	}
	if page2Body.Page.Next.Token != "" {
		t.Fatalf("page2 next cursor = %q, want empty body=%s", page2Body.Page.Next.Token, page2.Body.String())
	}
	assertNoUnsafeAuthorityBody(t, page2.Body.String())

	unsupportedReq := httptest.NewRequest(http.MethodGet, "/authority/decision-history?limit=1&rule_id=tenant.requests&from=2026-01-01T00:00:00Z", nil)
	unsupportedReq.Header.Set(diag.HeaderDiagnosticsSecret, cfg.Diagnostics.SharedSecret)
	unsupported := httptest.NewRecorder()
	h.ServeHTTP(unsupported, unsupportedReq)
	if unsupported.Code != http.StatusOK {
		t.Fatalf("unsupported status = %d body=%s", unsupported.Code, unsupported.Body.String())
	}
	unsupportedBody := decodeAuthorityPage[cp.AccountingDecisionRow](t, unsupported.Body.Bytes())
	if unsupportedBody.State != authorityapp.QueryStateUnsupported {
		t.Fatalf("unsupported state = %q body=%s", unsupportedBody.State, unsupported.Body.String())
	}
	if len(unsupportedBody.Page.Unsupported) != 1 || unsupportedBody.Page.Unsupported[0].Field != "time_range" {
		t.Fatalf("unsupported filters = %#v body=%s", unsupportedBody.Page.Unsupported, unsupported.Body.String())
	}
	assertNoUnsafeAuthorityBody(t, unsupported.Body.String())

	limitsUnsupportedReq := httptest.NewRequest(http.MethodGet, "/authority/limits?limit=1&rule_id=tenant.requests&settlement_state=settled", nil)
	limitsUnsupportedReq.Header.Set(diag.HeaderDiagnosticsSecret, cfg.Diagnostics.SharedSecret)
	limitsUnsupported := httptest.NewRecorder()
	h.ServeHTTP(limitsUnsupported, limitsUnsupportedReq)
	if limitsUnsupported.Code != http.StatusOK {
		t.Fatalf("limits unsupported status = %d body=%s", limitsUnsupported.Code, limitsUnsupported.Body.String())
	}
	limitsUnsupportedBody := decodeAuthorityPage[cp.AccountingLimitStatusRow](t, limitsUnsupported.Body.Bytes())
	if limitsUnsupportedBody.State != authorityapp.QueryStateUnsupported {
		t.Fatalf("limits unsupported state = %q body=%s", limitsUnsupportedBody.State, limitsUnsupported.Body.String())
	}
	if len(limitsUnsupportedBody.Page.Unsupported) != 1 || limitsUnsupportedBody.Page.Unsupported[0].Field != "settlement_state" {
		t.Fatalf("limits unsupported filters = %#v body=%s", limitsUnsupportedBody.Page.Unsupported, limitsUnsupported.Body.String())
	}
	assertNoUnsafeAuthorityBody(t, limitsUnsupported.Body.String())

	invalidReq := httptest.NewRequest(http.MethodGet, "/authority/decision-history?limit=bad", nil)
	invalidReq.Header.Set(diag.HeaderDiagnosticsSecret, cfg.Diagnostics.SharedSecret)
	invalid := httptest.NewRecorder()
	h.ServeHTTP(invalid, invalidReq)
	assertSafeAuthorityJSONError(t, invalid, http.StatusBadRequest, "invalid_query")

	tooBroadReq := httptest.NewRequest(http.MethodGet, "/authority/decision-history?limit=101", nil)
	tooBroadReq.Header.Set(diag.HeaderDiagnosticsSecret, cfg.Diagnostics.SharedSecret)
	tooBroad := httptest.NewRecorder()
	h.ServeHTTP(tooBroad, tooBroadReq)
	assertSafeAuthorityJSONError(t, tooBroad, http.StatusBadRequest, "too_broad")
}

func authorityHTTPConfig(enabled bool) *config.Config {
	return &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{DefaultRoute: "stub:model", MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{
			SharedSecret: strings.Repeat("s", 12),
		},
		Accounting: config.AccountingConfig{
			Authority: config.AccountingAuthorityConfig{
				Enabled:        enabled,
				Mode:           "strict",
				Store:          "memory",
				StartupPosture: "fail_closed",
				Query: config.AccountingAuthorityQueryConfig{
					Enabled:         enabled,
					PathPrefix:      "/authority",
					DefaultPageSize: 25,
					MaxPageSize:     50,
				},
				Rules: []config.AccountingAuthorityRuleConfig{
					{
						ID:    "tenant.requests",
						Kind:  "quota",
						Mode:  "strict",
						Unit:  "requests",
						Limit: 10,
						Match: config.AccountingAuthorityDimensionsConfig{
							Backend: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("backend-1")},
						},
					},
				},
			},
		},
	}
}

func authorityHTTPInput(t *testing.T, cfg *config.Config) (StandardHTTPInput, *authorityapp.Service) {
	t.Helper()
	src, err := configsource.New(cfg.Accounting.Authority)
	if err != nil {
		t.Fatal(err)
	}
	store := authoritystore.NewMemory(authoritystore.Config{
		Backing:   authoritydomain.BackingCapabilityAtomic,
		Readiness: authoritydomain.StatusFromBacking(authoritydomain.BackingCapabilityAtomic),
		LimitRows: []cp.AccountingLimitStatusRow{authorityHTTPLimitRow()},
	})
	svc := authorityapp.NewService(src, store, nil, nil)
	ex := runtime.TestExecutor()
	reg := pluginreg.NewRegistry()
	return StandardHTTPInput{
		Core:      HTTPCoreInput{Executor: ex},
		Frontends: frontendInputForTest(cfg, ex, reg),
		Security:  HTTPSecurityInput{UsageAuthority: cpadmin.AdaptAccountingAuthorityQueries(svc)},
	}, svc
}

func authorityHTTPLimitRow() cp.AccountingLimitStatusRow {
	return cp.AccountingLimitStatusRow{
		Correlation: cp.Correlation{
			TraceID:    "trace-1",
			RequestID:  "request-1",
			SessionID:  "session-1",
			ALegID:     "a-1",
			BLegID:     "b-1",
			AttemptSeq: 1,
			BackendID:  "backend-1",
			Model:      "model-1",
		},
		Scope: cp.ScopeSnapshot{
			Principal: scope.PrincipalScopeView{
				SubjectKind:    scope.SubjectUnknown,
				Origin:         scope.OriginInternal,
				PrincipalID:    scope.Known("principal-1"),
				TenantID:       scope.Known("tenant-1"),
				OrganizationID: scope.Known("organization-1"),
				WorkspaceID:    scope.Known("workspace-1"),
				ProjectID:      scope.Known("project-1"),
				DepartmentID:   scope.Known("department-1"),
				CostCenterID:   scope.Known("cost-center-1"),
			},
			PrincipalID:    scope.Known("principal-1"),
			CredentialID:   scope.Unknown(),
			TenantID:       scope.Known("tenant-1"),
			OrganizationID: scope.Known("organization-1"),
			WorkspaceID:    scope.Known("workspace-1"),
			ProjectID:      scope.Known("project-1"),
			DepartmentID:   scope.Known("department-1"),
			CostCenterID:   scope.Known("cost-center-1"),
		},
		RuleID:         "tenant.requests",
		RuleType:       "quota",
		Unit:           string(authoritydomain.AmountUnitRequests),
		Limit:          10,
		Remaining:      10,
		Authority:      cp.AccountingAuthoritySourceAuthoritative,
		EvidenceState:  cp.EvidenceRecorded,
		RedactionState: cp.RedactionSummarized,
	}
}

func seedAuthorityDecisionHistory(t *testing.T, svc *authorityapp.Service) {
	t.Helper()
	if svc == nil {
		t.Fatal("usage authority service is nil")
	}
	ctx := context.Background()
	inputs := []authorityapp.AdmissionInput{
		authorityHTTPAdmissionInput("request-1", "trace-1", "session-1", "a-1", "b-1", 1),
		authorityHTTPAdmissionInput("request-2", "trace-2", "session-2", "a-2", "b-2", 2),
	}
	for i, in := range inputs {
		if _, err := svc.Admit(ctx, in); err != nil {
			t.Fatalf("seed admission %d: %v", i+1, err)
		}
	}
}

func authorityHTTPAdmissionInput(requestID, traceID, sessionID, aLegID, bLegID string, attemptSeq int) authorityapp.AdmissionInput {
	dims := authorityHTTPDimensions()
	return authorityapp.AdmissionInput{
		Correlation: cp.Correlation{
			TraceID:    traceID,
			RequestID:  requestID,
			SessionID:  sessionID,
			ALegID:     aLegID,
			BLegID:     bLegID,
			AttemptSeq: attemptSeq,
			BackendID:  "backend-1",
			Model:      "model-1",
		},
		Scope:      authorityHTTPScopeView(),
		Dimensions: dims,
		Request:    authorityHTTPRequestAmount(),
		Spend:      authorityHTTPRequestAmount(),
		Authority:  authoritydomain.AuthorityLevelAuthoritative,
		ReservationKey: authoritydomain.ReservationKey{
			LogicalRequestID: requestID,
			ALegID:           aLegID,
			BLegID:           bLegID,
			AttemptID:        "attempt-" + strings.TrimPrefix(requestID, "request-"),
			RuleID:           "tenant.requests",
			Sequence:         1,
		},
	}
}

func authorityHTTPDimensions() authoritydomain.Dimensions {
	return authoritydomain.Dimensions{
		Principal:    scope.Known("principal-1"),
		Tenant:       scope.Known("tenant-1"),
		Organization: scope.Known("organization-1"),
		Workspace:    scope.Known("workspace-1"),
		Project:      scope.Known("project-1"),
		Department:   scope.Known("department-1"),
		CostCenter:   scope.Known("cost-center-1"),
		Backend:      scope.Known("backend-1"),
		Model:        scope.Known("model-1"),
		Route:        scope.Known("route-1"),
	}
}

func authorityHTTPScopeView() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind:    scope.SubjectUnknown,
		Origin:         scope.OriginInternal,
		PrincipalID:    scope.Known("principal-1"),
		CredentialID:   scope.Unknown(),
		TenantID:       scope.Known("tenant-1"),
		OrganizationID: scope.Known("organization-1"),
		WorkspaceID:    scope.Known("workspace-1"),
		ProjectID:      scope.Known("project-1"),
		DepartmentID:   scope.Known("department-1"),
		CostCenterID:   scope.Known("cost-center-1"),
	}
}

func authorityHTTPRequestAmount() authoritydomain.Amount {
	return authoritydomain.Amount{Unit: authoritydomain.AmountUnitRequests, Value: 1}
}

func decodeAuthorityPage[T any](t *testing.T, raw []byte) authorityPageResponse[T] {
	t.Helper()
	var out authorityPageResponse[T]
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode authority page: %v body=%s", err, string(raw))
	}
	return out
}

func assertSafeAuthorityJSONError(t *testing.T, rr *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if rr.Code != wantStatus {
		t.Fatalf("status = %d, want %d body=%s", rr.Code, wantStatus, rr.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v body=%s", err, rr.Body.String())
	}
	if len(body) != 1 || body["error"] != wantCode {
		t.Fatalf("error body = %#v, want code %q", body, wantCode)
	}
	for _, leak := range []string{"authoritystore", "matching limit", "limit must be positive", "backend not available"} {
		if strings.Contains(rr.Body.String(), leak) {
			t.Fatalf("error body leaked raw detail %q: %s", leak, rr.Body.String())
		}
	}
	assertNoUnsafeAuthorityBody(t, rr.Body.String())
}

func assertNoUnsafeAuthorityBody(t *testing.T, body string) {
	t.Helper()

	low := strings.ToLower(body)
	for _, leak := range []string{
		"bearer ",
		"api key",
		"api-key",
		"oauth",
		"resume token",
		"resume_token",
		"prompt",
		"response",
		"provider payload",
		"raw payload",
		"raw body",
		"raw headers",
		"authorization:",
		"sql",
		"driver",
		"dsn",
	} {
		if strings.Contains(low, leak) {
			t.Fatalf("authority response leaked forbidden substring %q: %s", leak, body)
		}
	}
}
