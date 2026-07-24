package lipruntime

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// legacyProductionRaterID maps deprecated Options.Rater alone (Task 8.4 deletes).
const legacyProductionRaterID = "legacy-production-rater"

// prepareCanonicalProduction adapts deprecated fields then validates registrations.
func prepareCanonicalProduction(opts Options) (normalizedProduction, error) {
	adapted, err := adaptLegacyOptions(opts)
	if err != nil {
		return normalizedProduction{}, err
	}
	return normalizeCanonicalOptions(adapted)
}

// adaptLegacyOptions is the sole current-major quarantine for deprecated
// provider/rater fields and authority ProviderDescriptors (req 10.5-10.6, 12.5).
func adaptLegacyOptions(opts Options) (Options, error) {
	for i, d := range opts.ProviderDescriptors {
		if err := d.Validate(); err != nil {
			return Options{}, fmt.Errorf("lipruntime: provider_descriptors[%d]: %w", i, err)
		}
	}
	out := opts
	if len(opts.RequestProviders) > 0 && len(opts.RequestRegistrations) > 0 {
		return Options{}, fmt.Errorf("lipruntime: cannot mix RequestProviders with RequestRegistrations")
	}
	if len(opts.AttemptProviders) > 0 && len(opts.AttemptRegistrations) > 0 {
		return Options{}, fmt.Errorf("lipruntime: cannot mix AttemptProviders with AttemptRegistrations")
	}
	if opts.ConcurrencyProvider != nil && opts.ConcurrencyRegistration != nil {
		return Options{}, fmt.Errorf("lipruntime: cannot mix ConcurrencyProvider with ConcurrencyRegistration")
	}
	if opts.Rater != nil && len(opts.RaterRegistrations) > 0 {
		return Options{}, fmt.Errorf("lipruntime: cannot mix Rater with RaterRegistrations")
	}
	requestDescs := filterDescriptorsByFamily(opts.ProviderDescriptors, stageFamilyRequest)
	attemptDescs := filterDescriptorsByFamily(opts.ProviderDescriptors, stageFamilyAttempt)
	leaseDescs := filterDescriptorsByFamily(opts.ProviderDescriptors, stageFamilyLease)
	if len(opts.RequestProviders) > 0 {
		regs, err := legacyRequestRegistrations(opts.RequestProviders, requestDescs)
		if err != nil {
			return Options{}, err
		}
		out.RequestRegistrations = regs
	}
	if len(opts.AttemptProviders) > 0 {
		regs, err := legacyAttemptRegistrations(opts.AttemptProviders, attemptDescs)
		if err != nil {
			return Options{}, err
		}
		out.AttemptRegistrations = regs
	}
	if opts.ConcurrencyProvider != nil {
		reg, err := legacyConcurrencyRegistration(opts.ConcurrencyProvider, leaseDescs)
		if err != nil {
			return Options{}, err
		}
		out.ConcurrencyRegistration = &reg
	}
	if opts.Rater != nil {
		out.RaterRegistrations = []economics.RaterRegistration{{
			ID: legacyProductionRaterID, Perspective: metering.PerspectiveOperator, Rater: opts.Rater,
		}}
	}
	out.RequestProviders = nil
	out.AttemptProviders = nil
	out.ConcurrencyProvider = nil
	out.Rater = nil
	out.ProviderDescriptors = nil
	return out, nil
}

type stageFamily int

const (
	stageFamilyRequest stageFamily = iota
	stageFamilyAttempt
	stageFamilyLease
)

func filterDescriptorsByFamily(in []authority.ProviderDescriptor, family stageFamily) []authority.ProviderDescriptor {
	var out []authority.ProviderDescriptor
	for _, d := range in {
		if d.EffectiveKind() != authority.ProviderKindObserver && descriptorHasFamily(d, family) {
			out = append(out, d)
		}
	}
	return out
}

func descriptorHasFamily(d authority.ProviderDescriptor, family stageFamily) bool {
	for _, p := range d.Postures {
		switch family {
		case stageFamilyRequest:
			switch p.Stage {
			case authority.StageRequestAdmit, authority.StageRequestSettle, authority.StageRequestRelease:
				return true
			}
		case stageFamilyAttempt:
			switch p.Stage {
			case authority.StageAttemptAdmit, authority.StageAttemptSettle, authority.StageAttemptRelease:
				return true
			}
		case stageFamilyLease:
			switch p.Stage {
			case authority.StageLeaseAdmit, authority.StageLeaseRelease:
				return true
			}
		}
	}
	return false
}

func legacyRequestRegistrations(providers []authority.RequestProvider, descs []authority.ProviderDescriptor) ([]authority.RequestRegistration, error) {
	if len(descs) != len(providers) {
		return nil, fmt.Errorf("lipruntime: legacy RequestProviders require matching request-stage ProviderDescriptors (got %d providers, %d descriptors)", len(providers), len(descs))
	}
	out := make([]authority.RequestRegistration, 0, len(providers))
	for i, p := range providers {
		if p == nil {
			return nil, fmt.Errorf("lipruntime: RequestProviders[%d]: nil provider", i)
		}
		out = append(out, authority.RequestRegistration{
			Descriptor: descs[i], Priority: authority.RequestPriorityQuotaBudgetRate, Provider: p,
		})
	}
	return out, nil
}

func legacyAttemptRegistrations(providers []authority.AttemptProvider, descs []authority.ProviderDescriptor) ([]authority.AttemptRegistration, error) {
	if len(descs) != len(providers) {
		return nil, fmt.Errorf("lipruntime: legacy AttemptProviders require matching attempt-stage ProviderDescriptors (got %d providers, %d descriptors)", len(providers), len(descs))
	}
	out := make([]authority.AttemptRegistration, 0, len(providers))
	for i, p := range providers {
		if p == nil {
			return nil, fmt.Errorf("lipruntime: AttemptProviders[%d]: nil provider", i)
		}
		out = append(out, authority.AttemptRegistration{
			Descriptor: descs[i], Priority: authority.AttemptPriorityHardSpend, Provider: p,
		})
	}
	return out, nil
}

func legacyConcurrencyRegistration(p authority.ConcurrencyProvider, descs []authority.ProviderDescriptor) (authority.ConcurrencyRegistration, error) {
	if p == nil {
		return authority.ConcurrencyRegistration{}, fmt.Errorf("lipruntime: nil ConcurrencyProvider")
	}
	if len(descs) != 1 {
		return authority.ConcurrencyRegistration{}, fmt.Errorf("lipruntime: legacy ConcurrencyProvider requires exactly one lease-stage ProviderDescriptor (got %d)", len(descs))
	}
	return authority.ConcurrencyRegistration{Descriptor: descs[0], Provider: p}, nil
}
