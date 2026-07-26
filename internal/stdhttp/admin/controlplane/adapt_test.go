package controlplane

import (
	"net/http"
	"net/http/httptest"
	"testing"

	concurrencyapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
)

func TestAdaptAccountingAuthorityQueries_typedNilIsNil(t *testing.T) {
	t.Parallel()
	if AdaptAccountingAuthorityQueries(nil) != nil {
		t.Fatal("nil *authorityapp.Service must adapt to nil AccountingAuthorityQueries")
	}
	var typedNil *authorityapp.Service
	got := AdaptAccountingAuthorityQueries(typedNil)
	if got != nil {
		t.Fatal("typed-nil *authorityapp.Service must adapt to nil AccountingAuthorityQueries")
	}
	concrete := &authorityapp.Service{}
	if AdaptAccountingAuthorityQueries(concrete) == nil {
		t.Fatal("non-nil *authorityapp.Service must adapt to non-nil interface")
	}
}

func TestAdaptConcurrencyAuthorityQueries_typedNilIsNil(t *testing.T) {
	t.Parallel()
	if AdaptConcurrencyAuthorityQueries(nil) != nil {
		t.Fatal("nil *concurrencyapp.Service must adapt to nil ConcurrencyAuthorityQueries")
	}
	var typedNil *concurrencyapp.Service
	got := AdaptConcurrencyAuthorityQueries(typedNil)
	if got != nil {
		t.Fatal("typed-nil *concurrencyapp.Service must adapt to nil ConcurrencyAuthorityQueries")
	}
	concrete := &concurrencyapp.Service{}
	if AdaptConcurrencyAuthorityQueries(concrete) == nil {
		t.Fatal("non-nil *concurrencyapp.Service must adapt to non-nil interface")
	}
}

func TestAdaptAccountingAuthorityQueries_typedNilLeavesRoutesDisabled(t *testing.T) {
	t.Parallel()
	var typedNil *authorityapp.Service
	h := NewAccountingAuthorityHandler(AuthorityOptions{
		Queries: AdaptAccountingAuthorityQueries(typedNil),
	})
	for _, path := range []string{"/", "/status", "/limits", "/decision-history"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d want 404 disabled; body=%s", path, rr.Code, rr.Body.String())
		}
	}
}

func TestAdaptConcurrencyAuthorityQueries_typedNilLeavesCapacityDisabled(t *testing.T) {
	t.Parallel()
	var typedNil *concurrencyapp.Service
	h := NewConcurrencyAuthorityHandler(ConcurrencyOptions{
		Service: AdaptConcurrencyAuthorityQueries(typedNil),
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/leases/capacity", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 disabled; body=%s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/leases/status", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("leases/status without provider/svc: status=%d want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestAdaptControlPlaneQueries_typedNilIsNil(t *testing.T) {
	t.Parallel()
	if AdaptControlPlaneQueries(nil) != nil {
		t.Fatal("nil *QueryService must adapt to nil Queries")
	}
	var typedNil *controlplane.QueryService
	if AdaptControlPlaneQueries(typedNil) != nil {
		t.Fatal("typed-nil *QueryService must adapt to nil Queries")
	}
	if AdaptControlPlaneQueries(&controlplane.QueryService{}) == nil {
		t.Fatal("non-nil *QueryService must adapt to non-nil interface")
	}
}

func TestAdaptReadinessReport_typedNilIsNil(t *testing.T) {
	t.Parallel()
	if AdaptReadinessReport(nil) != nil {
		t.Fatal("nil *ReadinessReportService must adapt to nil reader")
	}
	var typedNil *controlplane.ReadinessReportService
	if AdaptReadinessReport(typedNil) != nil {
		t.Fatal("typed-nil *ReadinessReportService must adapt to nil reader")
	}
	if AdaptReadinessReport(&controlplane.ReadinessReportService{}) == nil {
		t.Fatal("non-nil *ReadinessReportService must adapt to non-nil interface")
	}
}
