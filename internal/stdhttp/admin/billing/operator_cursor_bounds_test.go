package billing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// Finding 2 HTTP contract: a reader that enforces its Normalize contract
// rejects an oversized incoming cursor with the stable invalid-query
// classification, and the mounted operator route maps it to the fixed
// non-echoing wire error. The response body must be byte-stable and must never
// carry any caller cursor content.

type normalizingDetail struct{}

func (normalizingDetail) QueryEconomicDetail(_ context.Context, query corebilling.EconomicDetailQuery) (corebilling.EconomicDetail, error) {
	if _, err := query.Normalize(); err != nil {
		return corebilling.EconomicDetail{}, err
	}
	return corebilling.EconomicDetail{}, nil
}

type normalizingDiscrepancies struct{}

func (normalizingDiscrepancies) QueryDiscrepancies(_ context.Context, query economics.DiscrepancyQuery) (economics.DiscrepancyPage, error) {
	if _, err := query.Normalize(); err != nil {
		return economics.DiscrepancyPage{}, err
	}
	return economics.DiscrepancyPage{}, nil
}

type normalizingAllowances struct{}

func (normalizingAllowances) QueryAllowances(_ context.Context, query economics.AllowanceQuery) (economics.AllowancePage, error) {
	if _, err := query.Normalize(); err != nil {
		return economics.AllowancePage{}, err
	}
	return economics.AllowancePage{}, nil
}

type normalizingStatementLines struct{}

func (normalizingStatementLines) QueryStatementLines(_ context.Context, query economics.StatementLineQuery) (economics.StatementLinePage, error) {
	if _, err := query.Normalize(); err != nil {
		return economics.StatementLinePage{}, err
	}
	return economics.StatementLinePage{}, nil
}

type normalizingAdjustments struct{}

func (normalizingAdjustments) QueryAdjustments(_ context.Context, query economics.AdjustmentQuery) (economics.AdjustmentPage, error) {
	if _, err := query.Normalize(); err != nil {
		return economics.AdjustmentPage{}, err
	}
	return economics.AdjustmentPage{}, nil
}

func TestOperatorOversizedCursorHTTPRejectionIsStableAndNonEchoing(t *testing.T) {
	t.Parallel()

	opts := operatorTestOptions()
	opts.Operator.EconomicDetail = normalizingDetail{}
	opts.Operator.Discrepancies = normalizingDiscrepancies{}
	opts.Operator.Allowances = normalizingAllowances{}
	opts.Operator.StatementLines = normalizingStatementLines{}
	opts.Operator.Adjustments = normalizingAdjustments{}
	h := NewHandler(opts)

	validCallID := "bc_" + strings.Repeat("a", 32)
	cursor := "op2." + strings.Repeat("A", economics.MaxOperatorCursorBytes) + "." + strings.Repeat("B", 43)
	escaped := url.QueryEscape(cursor)

	routes := map[string]string{
		"economic detail": "/calls/" + validCallID + "/economics?store_id=test&account_id=acct&cursor=" + escaped,
		"discrepancy":     "/reconciliations?store_id=test&tenant_id=t&cursor=" + escaped,
		"allowance":       "/provider-accounts/provider-1/allowance-observations?store_id=test&tenant_id=t&cursor=" + escaped,
		"statement":       "/statement-lines?store_id=test&tenant_id=t&provider_account_key=p&cursor=" + escaped,
		"adjustment":      "/adjustments?store_id=test&account_id=acct&cursor=" + escaped,
	}

	const wantBody = "{\"error\":\"invalid_query\"}\n"
	for name, path := range routes {
		name, path := name, path
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
			if rec.Body.String() != wantBody {
				t.Fatalf("unstable or echoing body=%q want %q", rec.Body.String(), wantBody)
			}
			if strings.Contains(rec.Body.String(), strings.Repeat("A", 32)) {
				t.Fatalf("response echoed caller cursor content: %q", rec.Body.String())
			}
		})
	}
}
