package lipruntime

import (
	"context"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestLegacyOptions_RequestProviders_RequireExactDescriptors(t *testing.T) {
	t.Parallel()
	_, err := adaptLegacyOptions(Options{
		RequestProviders: []authority.RequestProvider{allowReq{}},
	})
	if err == nil {
		t.Fatal("expected cardinality error")
	}
	if !strings.Contains(err.Error(), "matching request-stage ProviderDescriptors") {
		t.Fatalf("err=%v", err)
	}
}

func TestLegacyOptions_RequestProviders_RejectNilProvider(t *testing.T) {
	t.Parallel()
	_, err := adaptLegacyOptions(Options{
		RequestProviders: []authority.RequestProvider{nil},
		ProviderDescriptors: []authority.ProviderDescriptor{
			requestDesc("req-1"),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "RequestProviders[0]: nil provider") {
		t.Fatalf("err=%v", err)
	}
}

func TestLegacyOptions_RequestProviders_PairsWithoutInventedIDs(t *testing.T) {
	t.Parallel()
	out, err := adaptLegacyOptions(Options{
		RequestProviders: []authority.RequestProvider{allowReq{}},
		ProviderDescriptors: []authority.ProviderDescriptor{
			requestDesc("legacy-paired-req"),
		},
	})
	if err != nil {
		t.Fatalf("adaptLegacyOptions: %v", err)
	}
	if len(out.RequestRegistrations) != 1 {
		t.Fatalf("regs=%d", len(out.RequestRegistrations))
	}
	if out.RequestRegistrations[0].Descriptor.ID != "legacy-paired-req" {
		t.Fatalf("id=%q", out.RequestRegistrations[0].Descriptor.ID)
	}
	if strings.HasPrefix(out.RequestRegistrations[0].Descriptor.ID, "production-request-") {
		t.Fatal("invented production-request id forbidden")
	}
	if len(out.RequestProviders) != 0 || out.ConcurrencyProvider != nil || out.Rater != nil {
		t.Fatal("adapted options must clear deprecated provider/rater fields")
	}
}

func TestLegacyOptions_AttemptProviders_RequireExactDescriptors(t *testing.T) {
	t.Parallel()
	_, err := adaptLegacyOptions(Options{
		AttemptProviders: []authority.AttemptProvider{allowAtt{}},
	})
	if err == nil || !strings.Contains(err.Error(), "matching attempt-stage ProviderDescriptors") {
		t.Fatalf("err=%v", err)
	}
}

func TestLegacyOptions_AttemptProviders_RejectNilProvider(t *testing.T) {
	t.Parallel()
	_, err := adaptLegacyOptions(Options{
		AttemptProviders: []authority.AttemptProvider{nil},
		ProviderDescriptors: []authority.ProviderDescriptor{
			attemptDesc("att-1"),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "AttemptProviders[0]: nil provider") {
		t.Fatalf("err=%v", err)
	}
}

func TestLegacyOptions_AttemptProviders_PairsWithoutInventedIDs(t *testing.T) {
	t.Parallel()
	out, err := adaptLegacyOptions(Options{
		AttemptProviders: []authority.AttemptProvider{allowAtt{}},
		ProviderDescriptors: []authority.ProviderDescriptor{
			attemptDesc("legacy-paired-att"),
		},
	})
	if err != nil {
		t.Fatalf("adaptLegacyOptions: %v", err)
	}
	if len(out.AttemptRegistrations) != 1 || out.AttemptRegistrations[0].Descriptor.ID != "legacy-paired-att" {
		t.Fatalf("regs=%+v", out.AttemptRegistrations)
	}
	if strings.HasPrefix(out.AttemptRegistrations[0].Descriptor.ID, "production-attempt-") {
		t.Fatal("invented production-attempt id forbidden")
	}
}

func TestLegacyOptions_ConcurrencyProvider_RequiresExactlyOneLeaseDescriptor(t *testing.T) {
	t.Parallel()
	_, err := adaptLegacyOptions(Options{
		ConcurrencyProvider: allowConc{},
	})
	if err == nil || !strings.Contains(err.Error(), "exactly one lease-stage ProviderDescriptor") {
		t.Fatalf("err=%v", err)
	}
}

func TestLegacyOptions_ConcurrencyProvider_RejectNil(t *testing.T) {
	t.Parallel()
	// Mixing path uses typed nil interface carefully: field left nil is "unset".
	// Explicit conversion helper rejects nil provider when called directly.
	_, err := legacyConcurrencyRegistration(nil, []authority.ProviderDescriptor{leaseDesc("lease-1")})
	if err == nil || !strings.Contains(err.Error(), "nil ConcurrencyProvider") {
		t.Fatalf("err=%v", err)
	}
}

func TestLegacyOptions_ConcurrencyProvider_PairsSingleLeaseDescriptor(t *testing.T) {
	t.Parallel()
	out, err := adaptLegacyOptions(Options{
		ConcurrencyProvider: allowConc{},
		ProviderDescriptors: []authority.ProviderDescriptor{leaseDesc("legacy-lease")},
	})
	if err != nil {
		t.Fatalf("adaptLegacyOptions: %v", err)
	}
	if out.ConcurrencyRegistration == nil || out.ConcurrencyRegistration.Descriptor.ID != "legacy-lease" {
		t.Fatalf("concurrency=%+v", out.ConcurrencyRegistration)
	}
	if out.ConcurrencyProvider != nil {
		t.Fatal("deprecated ConcurrencyProvider must be cleared")
	}
}

func TestLegacyOptions_Rater_MapsDeterministicID(t *testing.T) {
	t.Parallel()
	out, err := adaptLegacyOptions(Options{Rater: stubRater{}})
	if err != nil {
		t.Fatalf("adaptLegacyOptions: %v", err)
	}
	if len(out.RaterRegistrations) != 1 {
		t.Fatalf("raters=%d", len(out.RaterRegistrations))
	}
	if out.RaterRegistrations[0].ID != legacyProductionRaterID {
		t.Fatalf("id=%q want %q", out.RaterRegistrations[0].ID, legacyProductionRaterID)
	}
	if out.RaterRegistrations[0].Perspective != metering.PerspectiveOperator {
		t.Fatalf("perspective=%q", out.RaterRegistrations[0].Perspective)
	}
	if out.Rater != nil {
		t.Fatal("deprecated Rater must be cleared")
	}
}

func TestLegacyOptions_Rater_RejectsMixWithRegistrations(t *testing.T) {
	t.Parallel()
	_, err := adaptLegacyOptions(Options{
		Rater: stubRater{},
		RaterRegistrations: []economics.RaterRegistration{{
			ID: "explicit", Perspective: metering.PerspectiveOperator, Rater: stubRater{},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot mix Rater with RaterRegistrations") {
		t.Fatalf("err=%v", err)
	}
}

func TestLegacyOptions_RejectsMixingLegacyAndCanonical(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts Options
		want string
	}{
		{
			name: "request",
			opts: Options{
				RequestProviders: []authority.RequestProvider{allowReq{}},
				RequestRegistrations: []authority.RequestRegistration{{
					Descriptor: requestDesc("canonical"),
					Priority:   authority.RequestPriorityQuotaBudgetRate,
					Provider:   allowReq{},
				}},
			},
			want: "cannot mix RequestProviders with RequestRegistrations",
		},
		{
			name: "attempt",
			opts: Options{
				AttemptProviders: []authority.AttemptProvider{allowAtt{}},
				AttemptRegistrations: []authority.AttemptRegistration{{
					Descriptor: attemptDesc("canonical"),
					Priority:   authority.AttemptPriorityHardSpend,
					Provider:   allowAtt{},
				}},
			},
			want: "cannot mix AttemptProviders with AttemptRegistrations",
		},
		{
			name: "concurrency",
			opts: Options{
				ConcurrencyProvider: allowConc{},
				ConcurrencyRegistration: &authority.ConcurrencyRegistration{
					Descriptor: leaseDesc("canonical"),
					Provider:   allowConc{},
				},
			},
			want: "cannot mix ConcurrencyProvider with ConcurrencyRegistration",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := adaptLegacyOptions(tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want %q", err, tc.want)
			}
		})
	}
}

func TestLegacyOptions_ObserverOnlyDescriptors_ValidateWithoutBinding(t *testing.T) {
	t.Parallel()
	out, err := adaptLegacyOptions(Options{
		ProviderDescriptors: []authority.ProviderDescriptor{{
			ID:   "traffic-observer",
			Kind: authority.ProviderKindObserver,
			Postures: []authority.StagePosture{{
				Stage:           authority.StageRequestAdmit,
				Strength:        authority.StrengthAdvisory,
				FailureBehavior: authority.FailureFailOpen,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("adaptLegacyOptions: %v", err)
	}
	if len(out.RequestRegistrations)+len(out.AttemptRegistrations) != 0 || out.ConcurrencyRegistration != nil {
		t.Fatal("observer-only descriptors must not bind authority registrations")
	}
}

func TestLegacyOptions_ObserverRequiredStrength_Rejected(t *testing.T) {
	t.Parallel()
	_, err := adaptLegacyOptions(Options{
		ProviderDescriptors: []authority.ProviderDescriptor{{
			ID:   "bad-observer",
			Kind: authority.ProviderKindObserver,
			Postures: []authority.StagePosture{{
				Stage:           authority.StageRequestAdmit,
				Strength:        authority.StrengthRequired,
				FailureBehavior: authority.FailureFailClosed,
			}},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "observer cannot declare required strength") {
		t.Fatalf("err=%v", err)
	}
}

func TestCanonical_RegistrationsPassThroughWithoutLegacyHelpers(t *testing.T) {
	t.Parallel()
	in := Options{
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: requestDesc("canonical-req"),
			Priority:   authority.RequestPriorityQuotaBudgetRate,
			Provider:   allowReq{},
		}},
		AttemptRegistrations: []authority.AttemptRegistration{{
			Descriptor: attemptDesc("canonical-att"),
			Priority:   authority.AttemptPriorityHardSpend,
			Provider:   allowAtt{},
		}},
		ConcurrencyRegistration: &authority.ConcurrencyRegistration{
			Descriptor: leaseDesc("canonical-lease"),
			Provider:   allowConc{},
		},
		RaterRegistrations: []economics.RaterRegistration{{
			ID: "canonical-rater", Perspective: metering.PerspectiveOperator, Rater: stubRater{},
		}},
	}
	norm, err := prepareCanonicalProduction(in)
	if err != nil {
		t.Fatalf("prepareCanonicalProduction: %v", err)
	}
	if len(norm.RequestRegistrations) != 1 || norm.RequestRegistrations[0].Descriptor.ID != "canonical-req" {
		t.Fatalf("request=%+v", norm.RequestRegistrations)
	}
	if len(norm.AttemptRegistrations) != 1 || norm.AttemptRegistrations[0].Descriptor.ID != "canonical-att" {
		t.Fatalf("attempt=%+v", norm.AttemptRegistrations)
	}
	if norm.ConcurrencyRegistration == nil || norm.ConcurrencyRegistration.Descriptor.ID != "canonical-lease" {
		t.Fatalf("concurrency=%+v", norm.ConcurrencyRegistration)
	}
	if len(norm.RaterRegistrations) != 1 || norm.RaterRegistrations[0].ID != "canonical-rater" {
		t.Fatalf("rater=%+v", norm.RaterRegistrations)
	}
}

func requestDesc(id string) authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID:   id,
		Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage:           authority.StageRequestAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

func attemptDesc(id string) authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID:   id,
		Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage:           authority.StageAttemptAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

func leaseDesc(id string) authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID:   id,
		Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage:           authority.StageLeaseAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

type allowReq struct{}

func (allowReq) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}
func (allowReq) SettleRequest(_ context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}
func (allowReq) ReleaseRequest(context.Context, authority.RequestRelease) error { return nil }

type allowAtt struct{}

func (allowAtt) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}
func (allowAtt) SettleAttempt(_ context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}
func (allowAtt) ReleaseAttempt(context.Context, authority.AttemptRelease) error { return nil }

type allowConc struct{}

func (allowConc) AdmitLease(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "l"}, nil
}
func (allowConc) RenewLease(context.Context, authority.LeaseRenew) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "l"}, nil
}
func (allowConc) ReleaseLease(context.Context, authority.LeaseRelease) error { return nil }
func (allowConc) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}

type stubRater struct{}

func (stubRater) Rate(context.Context, economics.RatingRequest) (economics.RatingResult, error) {
	return economics.RatingResult{}, nil
}
