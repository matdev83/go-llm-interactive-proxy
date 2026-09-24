package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// Task 18.1 retirement guards (Migration Strategy step 8, req 15.6): the
// scalar-only live rating, destructive selected-event merge, and
// observer/token-ledger monetary writes retired by the V2 cutover must not be
// reintroduced. Checks are AST-scoped to the owning files so unrelated money
// vocabulary elsewhere cannot trip them.

func phase18ParseFile(t *testing.T, rel string) *ast.File {
	t.Helper()
	root := repoRoot(t)
	path := filepath.Join(root, filepath.FromSlash(rel))
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func phase18HasFuncDecl(file *ast.File, name string) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if decl, ok := n.(*ast.FuncDecl); ok && decl.Name.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

func phase18HasIdent(file *ast.File, name string) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

// TestPhase18NoScalarOnlyLiveRating rejects reintroduction of the V1
// token-to-money estimator and its live fallback wiring, on both the
// supplier and the customer paths. The authoritative provider-reported branch
// of RateProviderCost and the historical V1 readers are unaffected. The
// customer scalar engine (rateCustomerCharge/exactTokensAtRate) survives only
// behind the explicitly authorized V1 drain path; the production resolver and
// worker must carry the durable B1 pin owner into rating selection.
func TestPhase18NoScalarOnlyLiveRating(t *testing.T) {
	t.Parallel()
	rating := phase18ParseFile(t, "internal/core/billing/rating.go")
	if phase18HasFuncDecl(rating, "fallbackOperatorCost") {
		t.Fatal("internal/core/billing/rating.go defines fallbackOperatorCost: scalar-only live rating is retired")
	}
	providerCost := phase18ParseFile(t, "internal/core/billing/provider_cost.go")
	if phase18HasIdent(providerCost, "fallbackOperatorCost") {
		t.Fatal("internal/core/billing/provider_cost.go references fallbackOperatorCost: scalar fallback wiring is retired")
	}
	resolver := phase18ParseFile(t, "internal/infra/billingcompose/resolver.go")
	if phase18HasIdent(resolver, "OperatorRate") {
		t.Fatal("internal/infra/billingcompose/resolver.go references OperatorRate: catalog-rate live fallback is retired")
	}
	// Customer path (Phase 18 blocker 1): the scalar live engine must be
	// isolated behind the V1 drain contract and unreachable for V2 owner.
	callRating := phase18ParseFile(t, "internal/core/billing/call_rating.go")
	for _, required := range []string{
		"effectiveCallRatingOwner",
		"rateV2ComponentCall",
		"rateV1DrainCall",
		"ValidateCallRatingResultForOwner",
		"ValidateCallRatingResultForSettlement",
		"OwnerAwareCallRatingResolver",
	} {
		if !phase18HasFuncDecl(callRating, required) && !phase18HasIdent(callRating, required) {
			t.Fatalf("internal/core/billing/call_rating.go lacks %s: owner-aware V1/V2 selection is required", required)
		}
	}
	if phase18FuncBodyHasIdent(callRating, "RateCall", "rateCustomerCharge") {
		t.Fatal("internal/core/billing/call_rating.go RateCall calls rateCustomerCharge: scalar engine must live only in rateV1DrainCall behind V1 ownership")
	}
	if !phase18FuncBodyHasIdent(callRating, "rateV1DrainCall", "rateCustomerCharge") {
		t.Fatal("internal/core/billing/call_rating.go rateV1DrainCall must retain rateCustomerCharge for authorized V1 drain/replay")
	}
	if !phase18FuncBodyHasIdent(callRating, "rateV2ComponentCall", "RateSelectedRetailBLegs") {
		t.Fatal("internal/core/billing/call_rating.go rateV2ComponentCall must rate via RateSelectedRetailBLegs (component path, including mapped legacy semantics)")
	}
	if phase18HasIdent(callRating, "LegacyScalarSemantics") && !phase18FuncBodyHasIdent(callRating, "rateV1DrainCall", "isLegacyScalarTariff") {
		t.Fatal("internal/core/billing/call_rating.go must gate the legacy tag to the V1 drain path, never to V2 component selection")
	}
	if !phase18HasFuncDecl(resolver, "ResolveCallRatingForOwner") {
		t.Fatal("internal/infra/billingcompose/resolver.go lacks ResolveCallRatingForOwner: production resolver must propagate the B1 pin owner into rating")
	}
	if !phase18HasIdent(resolver, "ValidateCallRatingResultForSettlement") {
		t.Fatal("internal/infra/billingcompose/resolver.go must enforce ValidateCallRatingResultForSettlement for bound V2 component valuation")
	}
	worker := phase18ParseFile(t, "internal/core/billing/call_post_usage_worker.go")
	if !phase18HasIdent(worker, "ResolveCallRatingForOwner") {
		t.Fatal("internal/core/billing/call_post_usage_worker.go must propagate the claim owner via ResolveCallRatingForOwner")
	}
	if !phase18HasIdent(worker, "OwnerAwareCallRatingResolver") {
		t.Fatal("internal/core/billing/call_post_usage_worker.go must resolve via OwnerAwareCallRatingResolver")
	}
	if !phase18HasIdent(worker, "ValidateCallRatingResultForSettlement") {
		t.Fatal("internal/core/billing/call_post_usage_worker.go must enforce ValidateCallRatingResultForSettlement before Apply (generic bound V2 result fence)")
	}
	settlement := phase18ParseFile(t, "internal/infra/billingstore/call_settlement.go")
	if !phase18HasIdent(settlement, "ValidateCallRatingResultForSettlement") {
		t.Fatal("internal/infra/billingstore/call_settlement.go must enforce ValidateCallRatingResultForSettlement at transaction entry")
	}
}

func phase18FuncBodyHasIdent(file *ast.File, funcName, ident string) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		decl, ok := n.(*ast.FuncDecl)
		if !ok || decl.Name.Name != funcName {
			return true
		}
		ast.Inspect(decl.Body, func(m ast.Node) bool {
			if id, ok := m.(*ast.Ident); ok && id.Name == ident {
				found = true
				return false
			}
			return true
		})
		return false
	})
	return found
}

// TestPhase18NoDestructiveSelectedEventMerge rejects the legacy destructive
// cost-merge helper while requiring the explicit one-way V1 compatibility
// projection to remain available for historical readers.
func TestPhase18NoDestructiveSelectedEventMerge(t *testing.T) {
	t.Parallel()
	file := phase18ParseFile(t, "internal/core/runtime/billing_leg.go")
	if phase18HasFuncDecl(file, "mergeStreamCostOntoLeg") {
		t.Fatal("internal/core/runtime/billing_leg.go defines mergeStreamCostOntoLeg: destructive selected-event merge is retired")
	}
	if !phase18HasFuncDecl(file, "projectV1BillingEvidence") {
		t.Fatal("internal/core/runtime/billing_leg.go lost projectV1BillingEvidence: explicit one-way V1 projection must be retained")
	}
}

// TestPhase18ObserversCannotWriteMoney keeps the runtime billing observer a
// non-monetary seam. Terminal durability belongs to TerminalUsageSink; the
// observer must not gain journal/store posting calls.
func TestPhase18ObserversCannotWriteMoney(t *testing.T) {
	t.Parallel()
	for _, rel := range []string{
		"internal/core/runtime/billing_collector.go",
		"internal/infra/runtimebundle/billing_leg_observer.go",
	} {
		file := phase18ParseFile(t, rel)
		for _, forbidden := range []string{
			"ApplyProviderCost",
			"ApplyCallBillingResult",
			"MarkProviderCostUnreconciled",
			"postJournalInTx",
			"postJournalTransaction",
		} {
			if phase18HasIdent(file, forbidden) {
				t.Fatalf("%s references %s: billing observers must not write money", rel, forbidden)
			}
		}
	}
}
