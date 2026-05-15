package domain

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestReconcileProviderOnlySelectedForProviderBilling(t *testing.T) {
	t.Parallel()

	provider := usage(10, 20, lipapi.UsagePlaneProviderBillable, lipapi.UsageSourceProviderReported, lipapi.UsageAuthorityAuthoritative)

	got := Reconcile(PolicyBillProvider, provider)

	if !got.Complete {
		t.Fatalf("complete = false, reason %q", got.IncompleteReason)
	}
	if got.BillablePlane != lipapi.UsagePlaneProviderBillable {
		t.Fatalf("billable plane = %q", got.BillablePlane)
	}
	assertUsage(t, got.BillableUsage, 10, 20, 30)
	plane, ok := got.UsageForPlane(lipapi.UsagePlaneProviderBillable)
	if !ok {
		t.Fatal("provider plane missing")
	}
	assertUsage(t, plane, 10, 20, 30)
}

func TestReconcileTransformedResponsePreservesProviderAndClientVisibleUsage(t *testing.T) {
	t.Parallel()

	provider := usage(100, 50, lipapi.UsagePlaneProviderBillable, lipapi.UsageSourceProviderReported, lipapi.UsageAuthorityAuthoritative)
	client := usage(80, 40, lipapi.UsagePlaneClientVisible, lipapi.UsageSourceProxyAdjusted, lipapi.UsageAuthorityDelegated)

	tests := []struct {
		name      string
		policy    BillingPolicy
		wantPlane lipapi.UsagePlane
		wantIn    int
		wantOut   int
	}{
		{name: "provider billing", policy: PolicyBillProvider, wantPlane: lipapi.UsagePlaneProviderBillable, wantIn: 100, wantOut: 50},
		{name: "client visible billing", policy: PolicyBillClientVisible, wantPlane: lipapi.UsagePlaneClientVisible, wantIn: 80, wantOut: 40},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Reconcile(tt.policy, provider, client)

			if !got.Complete {
				t.Fatalf("complete = false, reason %q", got.IncompleteReason)
			}
			if got.BillablePlane != tt.wantPlane {
				t.Fatalf("billable plane = %q, want %q", got.BillablePlane, tt.wantPlane)
			}
			assertUsage(t, got.BillableUsage, tt.wantIn, tt.wantOut, tt.wantIn+tt.wantOut)
			assertHasConflict(t, got.Conflicts, ConflictTransformedUsage)
			if _, ok := got.UsageForPlane(lipapi.UsagePlaneProviderBillable); !ok {
				t.Fatal("provider plane missing")
			}
			if _, ok := got.UsageForPlane(lipapi.UsagePlaneClientVisible); !ok {
				t.Fatal("client-visible plane missing")
			}
		})
	}
}

func TestReconcileLocalEstimateDoesNotOverwriteProviderAuthoritativeUsage(t *testing.T) {
	t.Parallel()

	provider := usage(100, 50, lipapi.UsagePlaneProviderBillable, lipapi.UsageSourceProviderReported, lipapi.UsageAuthorityAuthoritative)
	estimate := usage(95, 45, lipapi.UsagePlaneProviderBillable, lipapi.UsageSourceLocalEstimator, lipapi.UsageAuthorityEstimated)

	got := Reconcile(PolicyBillProvider, estimate, provider)

	if !got.Complete {
		t.Fatalf("complete = false, reason %q", got.IncompleteReason)
	}
	assertUsage(t, got.BillableUsage, 100, 50, 150)
	assertHasConflict(t, got.Conflicts, ConflictPlaneCandidates)
}

func TestReconcileStrictProviderRequiredMissingUsageIncomplete(t *testing.T) {
	t.Parallel()

	client := usage(20, 10, lipapi.UsagePlaneClientVisible, lipapi.UsageSourceProxyAdjusted, lipapi.UsageAuthorityDelegated)

	got := Reconcile(PolicyStrictProviderRequired, client)

	if got.Complete {
		t.Fatal("complete = true")
	}
	if !strings.Contains(got.IncompleteReason, string(lipapi.UsagePlaneProviderBillable)) {
		t.Fatalf("incomplete reason = %q", got.IncompleteReason)
	}
	if got.BillablePlane != lipapi.UsagePlaneUnknown {
		t.Fatalf("billable plane = %q", got.BillablePlane)
	}
}

func TestReconcileStrictProviderRequiredRejectsLocalProviderEstimate(t *testing.T) {
	t.Parallel()

	estimate := usage(20, 10, lipapi.UsagePlaneProviderBillable, lipapi.UsageSourceLocalEstimator, lipapi.UsageAuthorityEstimated)

	got := Reconcile(PolicyStrictProviderRequired, estimate)

	if got.Complete {
		t.Fatal("complete = true")
	}
	if !strings.Contains(got.IncompleteReason, "authoritative provider-reported") {
		t.Fatalf("incomplete reason = %q", got.IncompleteReason)
	}
	if got.BillablePlane != lipapi.UsagePlaneUnknown {
		t.Fatalf("billable plane = %q", got.BillablePlane)
	}
}

func TestReconcileStrictProviderRequiredAcceptsAuthoritativeProviderSources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source lipapi.UsageSource
	}{
		{name: "provider reported", source: lipapi.UsageSourceProviderReported},
		{name: "provider count api", source: lipapi.UsageSourceProviderCountAPI},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := usage(20, 10, lipapi.UsagePlaneProviderBillable, tt.source, lipapi.UsageAuthorityAuthoritative)

			got := Reconcile(PolicyStrictProviderRequired, provider)

			if !got.Complete {
				t.Fatalf("complete = false, reason %q", got.IncompleteReason)
			}
			assertUsage(t, got.BillableUsage, 20, 10, 30)
		})
	}
}

func TestReconcileStrictProviderRequiredRejectsDelegatedProviderReportedUsage(t *testing.T) {
	t.Parallel()

	delegated := usage(20, 10, lipapi.UsagePlaneProviderBillable, lipapi.UsageSourceProviderReported, lipapi.UsageAuthorityDelegated)

	got := Reconcile(PolicyStrictProviderRequired, delegated)

	if got.Complete {
		t.Fatal("complete = true")
	}
	if !strings.Contains(got.IncompleteReason, "authoritative provider-reported") {
		t.Fatalf("incomplete reason = %q", got.IncompleteReason)
	}
}

func TestReconcileEventsDeduplicatesCandidatesAndKeepsPolicyReservedAdditive(t *testing.T) {
	t.Parallel()

	provider := usage(12, 8, lipapi.UsagePlaneProviderBillable, lipapi.UsageSourceProviderReported, lipapi.UsageAuthorityAuthoritative)
	reservedA := usage(3, 2, lipapi.UsagePlaneProxyBillable, lipapi.UsageSourcePolicyReserved, lipapi.UsageAuthorityAuthoritative)
	reservedB := usage(4, 1, lipapi.UsagePlaneProxyBillable, lipapi.UsageSourcePolicyReserved, lipapi.UsageAuthorityAuthoritative)
	events := []lipapi.Event{
		{Kind: lipapi.EventUsageDelta, UsageScopes: []lipapi.ScopedUsageDelta{provider, reservedA}},
		{Kind: lipapi.EventUsageDelta, UsageScopes: []lipapi.ScopedUsageDelta{provider, reservedB}},
	}

	providerResult := ReconcileEvents(PolicyBillProvider, events...)
	assertUsage(t, providerResult.BillableUsage, 12, 8, 20)

	proxyResult := ReconcileEvents(PolicyBillProxyBillable, events...)
	if !proxyResult.Complete {
		t.Fatalf("complete = false, reason %q", proxyResult.IncompleteReason)
	}
	assertUsage(t, proxyResult.BillableUsage, 7, 3, 10)
}

func TestReconcilePolicyReservedDoesNotHideLaterActualUsageForSamePlane(t *testing.T) {
	t.Parallel()

	reserved := usage(3, 2, lipapi.UsagePlaneProxyBillable, lipapi.UsageSourcePolicyReserved, lipapi.UsageAuthorityAuthoritative)
	actual := usage(12, 8, lipapi.UsagePlaneProxyBillable, lipapi.UsageSourceProxyAdjusted, lipapi.UsageAuthorityDelegated)

	got := Reconcile(PolicyBillProxyBillable, reserved, actual)

	if !got.Complete {
		t.Fatalf("complete = false, reason %q", got.IncompleteReason)
	}
	assertUsage(t, got.BillableUsage, 12, 8, 20)
	assertHasConflict(t, got.Conflicts, ConflictPlaneCandidates)
}

func TestReconcilePolicyReservedDoesNotInflateProviderActualUsageForSamePlane(t *testing.T) {
	t.Parallel()

	reserved := usage(5, 5, lipapi.UsagePlaneProviderBillable, lipapi.UsageSourcePolicyReserved, lipapi.UsageAuthorityAuthoritative)
	provider := usage(100, 50, lipapi.UsagePlaneProviderBillable, lipapi.UsageSourceProviderReported, lipapi.UsageAuthorityAuthoritative)

	got := Reconcile(PolicyBillProvider, reserved, provider)

	if !got.Complete {
		t.Fatalf("complete = false, reason %q", got.IncompleteReason)
	}
	assertUsage(t, got.BillableUsage, 100, 50, 150)
	assertHasConflict(t, got.Conflicts, ConflictPlaneCandidates)
}

func TestReconcileUnknownPlaneAndSourceAreExplicitWarnings(t *testing.T) {
	t.Parallel()

	unknown := usage(0, 0, lipapi.UsagePlaneUnknown, lipapi.UsageSourceUnknown, lipapi.UsageAuthorityUnknown)

	got := Reconcile(PolicyBillProvider, unknown)

	if got.Complete {
		t.Fatal("complete = true")
	}
	assertHasWarning(t, got.Warnings, WarningUnknownPlane)
	assertHasWarning(t, got.Warnings, WarningUnknownSource)
	assertHasWarning(t, got.Warnings, WarningZeroUsage)
}

func usage(input, output int, plane lipapi.UsagePlane, source lipapi.UsageSource, authority lipapi.UsageAuthority) lipapi.ScopedUsageDelta {
	return lipapi.ScopedUsageDelta{
		InputTokens:  input,
		OutputTokens: output,
		TotalTokens:  input + output,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:     plane,
			Source:    source,
			Authority: authority,
		},
	}
}

func assertUsage(t *testing.T, got lipapi.ScopedUsageDelta, input, output, total int) {
	t.Helper()
	if got.InputTokens != input || got.OutputTokens != output || got.TotalTokens != total {
		t.Fatalf("usage = input %d output %d total %d, want input %d output %d total %d", got.InputTokens, got.OutputTokens, got.TotalTokens, input, output, total)
	}
}

func assertHasConflict(t *testing.T, conflicts []Conflict, code ConflictCode) {
	t.Helper()
	for _, conflict := range conflicts {
		if conflict.Code == code {
			return
		}
	}
	t.Fatalf("conflict %q missing from %#v", code, conflicts)
}

func assertHasWarning(t *testing.T, warnings []Warning, code WarningCode) {
	t.Helper()
	for _, warning := range warnings {
		if warning.Code == code {
			return
		}
	}
	t.Fatalf("warning %q missing from %#v", code, warnings)
}
