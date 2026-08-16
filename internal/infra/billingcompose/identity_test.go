package billingcompose_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestPrincipalSessionIdentity(t *testing.T) {
	t.Parallel()

	stubRefs := billingcompose.SnapshotRefFuncs{
		CustomerPricingRef: func(context.Context, lipapi.Call) billing.VersionRef {
			return billing.VersionRef{ID: "pricing", Version: "v1"}
		},
		ChargePolicyRef: func(context.Context, lipapi.Call) billing.VersionRef {
			return billing.VersionRef{ID: "policy", Version: "v2"}
		},
		OperatorRateRef: func(_ context.Context, backend, model string) billing.VersionRef {
			return billing.VersionRef{ID: backend + "/" + model, Version: "op-1"}
		},
	}

	tests := []struct {
		name        string
		ctx         context.Context
		call        lipapi.Call
		wantAccount string
	}{
		{
			name: "maps principal to account without creating an account",
			ctx: scope.WithScope(context.Background(), scope.PrincipalScopeView{
				PrincipalID: scope.Known("acct-42"),
			}),
			call: lipapi.Call{Session: lipapi.SessionRef{
				AuthoritativeSessionID: "sess-auth",
				ClientSessionID:        "client-hint-must-ignore",
			}},
			wantAccount: "acct-42",
		},
		{
			name: "missing principal yields empty account",
			ctx:  context.Background(),
			call: lipapi.Call{Session: lipapi.SessionRef{
				AuthoritativeSessionID: "sess-auth",
			}},
			wantAccount: "",
		},
		{
			name: "unknown principal yields empty account",
			ctx: scope.WithScope(context.Background(), scope.PrincipalScopeView{
				PrincipalID: scope.Unknown(),
			}),
			call: lipapi.Call{Session: lipapi.SessionRef{
				AuthoritativeSessionID: "sess-auth",
			}},
			wantAccount: "",
		},
		{
			name: "blank principal yields empty account",
			ctx: scope.WithScope(context.Background(), scope.PrincipalScopeView{
				PrincipalID: scope.Known(""),
			}),
			call: lipapi.Call{Session: lipapi.SessionRef{
				AuthoritativeSessionID: "sess-auth",
			}},
			wantAccount: "",
		},
		{
			name: "whitespace-only principal yields empty account",
			ctx: scope.WithScope(context.Background(), scope.PrincipalScopeView{
				PrincipalID: scope.Known("   "),
			}),
			call: lipapi.Call{Session: lipapi.SessionRef{
				AuthoritativeSessionID: "sess-auth",
			}},
			wantAccount: "",
		},
		{
			name: "trims principal id",
			ctx: scope.WithScope(context.Background(), scope.PrincipalScopeView{
				PrincipalID: scope.Known("  acct-42  "),
			}),
			call: lipapi.Call{Session: lipapi.SessionRef{
				AuthoritativeSessionID: "sess-auth",
			}},
			wantAccount: "acct-42",
		},
		{
			name: "client-hint-only session yields empty authorization",
			ctx: scope.WithScope(context.Background(), scope.PrincipalScopeView{
				PrincipalID: scope.Known("acct-42"),
			}),
			call: lipapi.Call{Session: lipapi.SessionRef{
				ClientSessionID: "only-client-hint",
			}},
			wantAccount: "acct-42",
		},
		{
			name: "missing authoritative session yields empty authorization",
			ctx: scope.WithScope(context.Background(), scope.PrincipalScopeView{
				PrincipalID: scope.Known("acct-42"),
			}),
			call:        lipapi.Call{},
			wantAccount: "acct-42",
		},
		{
			name: "empty A-leg yields empty authorization",
			ctx: scope.WithScope(context.Background(), scope.PrincipalScopeView{
				PrincipalID: scope.Known("acct-42"),
			}),
			call: lipapi.Call{Session: lipapi.SessionRef{
				AuthoritativeSessionID: "sess-auth",
				ALegID:                 "hint-aleg",
			}},
			wantAccount: "acct-42",
		},
		{
			name: "whitespace A-leg yields empty authorization",
			ctx: scope.WithScope(context.Background(), scope.PrincipalScopeView{
				PrincipalID: scope.Known("acct-42"),
			}),
			call: lipapi.Call{Session: lipapi.SessionRef{
				AuthoritativeSessionID: "sess-auth",
			}},
			wantAccount: "acct-42",
		},
		{
			name: "trims authoritative session and A-leg",
			ctx: scope.WithScope(context.Background(), scope.PrincipalScopeView{
				PrincipalID: scope.Known("acct-42"),
			}),
			call: lipapi.Call{Session: lipapi.SessionRef{
				AuthoritativeSessionID: "  sess-auth  ",
				ClientSessionID:        "client-hint",
			}},
			wantAccount: "acct-42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id := billingcompose.PrincipalSessionIdentity(stubRefs)
			assertIdentityResolvers(t, id)
			gotAccount := id.AccountID(tt.ctx, tt.call)
			if gotAccount != tt.wantAccount {
				t.Errorf("AccountID = %q, want %q", gotAccount, tt.wantAccount)
			}
			assertStubSnapshotRefs(t, id, stubRefs)
		})
	}

	t.Run("stamps snapshot refs from catalog methods", func(t *testing.T) {
		t.Parallel()
		c, pricing, policy, rates := seedCatalog(t)
		if err := c.SetOperatorRateBinding("backend", "model", rates.Ref); err != nil {
			t.Fatal(err)
		}
		id := billingcompose.PrincipalSessionIdentity(billingcompose.SnapshotRefFuncs{
			CustomerPricingRef: c.CustomerPricingRef,
			ChargePolicyRef:    c.ChargePolicyRef,
			OperatorRateRef:    c.OperatorRateRef,
		})
		assertIdentityResolvers(t, id)
		if id.CustomerPricingRef == nil || id.ChargePolicyRef == nil || id.OperatorRateRef == nil {
			t.Fatal("catalog snapshot ref funcs must be stamped")
		}
		ctx := context.Background()
		call := lipapi.Call{ID: "call-1"}
		if got := id.CustomerPricingRef(ctx, call); !versionIdentityEqual(got, pricing.Ref) {
			t.Fatalf("CustomerPricingRef = %+v, want %+v", got, pricing.Ref)
		}
		if got := id.ChargePolicyRef(ctx, call); !versionIdentityEqual(got, policy.Ref) {
			t.Fatalf("ChargePolicyRef = %+v, want %+v", got, policy.Ref)
		}
		if got := id.OperatorRateRef(ctx, "backend", "model"); !versionIdentityEqual(got, rates.Ref) {
			t.Fatalf("OperatorRateRef = %+v, want %+v", got, rates.Ref)
		}
		if got := id.OperatorRateRef(ctx, "backend", "unbound"); got != (billing.VersionRef{}) {
			t.Fatalf("unbound OperatorRateRef = %+v, want empty", got)
		}
	})
}

func assertIdentityResolvers(t *testing.T, id runtime.BillingIdentity) {
	t.Helper()
	if id.AccountID == nil {
		t.Fatal("AccountID resolver is required")
	}
}

func assertStubSnapshotRefs(t *testing.T, id runtime.BillingIdentity, refs billingcompose.SnapshotRefFuncs) {
	t.Helper()
	if id.CustomerPricingRef == nil || id.ChargePolicyRef == nil || id.OperatorRateRef == nil {
		t.Fatal("snapshot ref funcs must be stamped from the supplied stubs")
	}
	ctx := context.Background()
	call := lipapi.Call{ID: "stamp"}
	if got, want := id.CustomerPricingRef(ctx, call), refs.CustomerPricingRef(ctx, call); got != want {
		t.Errorf("CustomerPricingRef = %+v, want %+v", got, want)
	}
	if got, want := id.ChargePolicyRef(ctx, call), refs.ChargePolicyRef(ctx, call); got != want {
		t.Errorf("ChargePolicyRef = %+v, want %+v", got, want)
	}
	if got, want := id.OperatorRateRef(ctx, "backend", "model"), refs.OperatorRateRef(ctx, "backend", "model"); got != want {
		t.Errorf("OperatorRateRef = %+v, want %+v", got, want)
	}
}
