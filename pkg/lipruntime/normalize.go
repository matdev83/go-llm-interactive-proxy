package lipruntime

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const legacyProductionRaterID = "legacy-production-rater"

// normalizedProduction is the descriptor-bound production injection set after
// validating new registrations and adapting deprecated parallel slices.
type normalizedProduction struct {
	RequestRegistrations    []authority.RequestRegistration
	AttemptRegistrations    []authority.AttemptRegistration
	ConcurrencyRegistration *authority.ConcurrencyRegistration
	RaterRegistrations      []economics.RaterRegistration
}

func normalizeOptions(opts Options) (normalizedProduction, error) {
	for i, d := range opts.ProviderDescriptors {
		if err := d.Validate(); err != nil {
			return normalizedProduction{}, fmt.Errorf("lipruntime: provider_descriptors[%d]: %w", i, err)
		}
	}

	out := normalizedProduction{
		RequestRegistrations:    append([]authority.RequestRegistration(nil), opts.RequestRegistrations...),
		AttemptRegistrations:    append([]authority.AttemptRegistration(nil), opts.AttemptRegistrations...),
		ConcurrencyRegistration: opts.ConcurrencyRegistration,
		RaterRegistrations:      append([]economics.RaterRegistration(nil), opts.RaterRegistrations...),
	}

	if len(opts.RequestProviders) > 0 && len(opts.RequestRegistrations) > 0 {
		return normalizedProduction{}, fmt.Errorf("lipruntime: cannot mix RequestProviders with RequestRegistrations")
	}
	if len(opts.AttemptProviders) > 0 && len(opts.AttemptRegistrations) > 0 {
		return normalizedProduction{}, fmt.Errorf("lipruntime: cannot mix AttemptProviders with AttemptRegistrations")
	}
	if opts.ConcurrencyProvider != nil && opts.ConcurrencyRegistration != nil {
		return normalizedProduction{}, fmt.Errorf("lipruntime: cannot mix ConcurrencyProvider with ConcurrencyRegistration")
	}
	if opts.Rater != nil && len(opts.RaterRegistrations) > 0 {
		return normalizedProduction{}, fmt.Errorf("lipruntime: cannot mix Rater with RaterRegistrations")
	}

	requestDescs := filterDescriptorsByFamily(opts.ProviderDescriptors, stageFamilyRequest)
	attemptDescs := filterDescriptorsByFamily(opts.ProviderDescriptors, stageFamilyAttempt)
	leaseDescs := filterDescriptorsByFamily(opts.ProviderDescriptors, stageFamilyLease)

	if len(opts.RequestProviders) > 0 {
		regs, err := legacyRequestRegistrations(opts.RequestProviders, requestDescs)
		if err != nil {
			return normalizedProduction{}, err
		}
		out.RequestRegistrations = regs
	}
	if len(opts.AttemptProviders) > 0 {
		regs, err := legacyAttemptRegistrations(opts.AttemptProviders, attemptDescs)
		if err != nil {
			return normalizedProduction{}, err
		}
		out.AttemptRegistrations = regs
	}
	if opts.ConcurrencyProvider != nil {
		reg, err := legacyConcurrencyRegistration(opts.ConcurrencyProvider, leaseDescs)
		if err != nil {
			return normalizedProduction{}, err
		}
		out.ConcurrencyRegistration = &reg
	}
	if opts.Rater != nil {
		out.RaterRegistrations = []economics.RaterRegistration{{
			ID:          legacyProductionRaterID,
			Perspective: metering.PerspectiveOperator,
			Rater:       opts.Rater,
		}}
	}

	if err := validateRegistrationSets(out); err != nil {
		return normalizedProduction{}, err
	}
	return out, nil
}

func validateRegistrationSets(n normalizedProduction) error {
	seenReq := make(map[string]struct{}, len(n.RequestRegistrations))
	for i, reg := range n.RequestRegistrations {
		if err := reg.Validate(); err != nil {
			return fmt.Errorf("lipruntime: request_registrations[%d]: %w", i, err)
		}
		id := strings.TrimSpace(reg.Descriptor.ID)
		if _, dup := seenReq[id]; dup {
			return fmt.Errorf("lipruntime: duplicate request registration id %q", id)
		}
		seenReq[id] = struct{}{}
	}
	seenAtt := make(map[string]struct{}, len(n.AttemptRegistrations))
	for i, reg := range n.AttemptRegistrations {
		if err := reg.Validate(); err != nil {
			return fmt.Errorf("lipruntime: attempt_registrations[%d]: %w", i, err)
		}
		id := strings.TrimSpace(reg.Descriptor.ID)
		if _, dup := seenAtt[id]; dup {
			return fmt.Errorf("lipruntime: duplicate attempt registration id %q", id)
		}
		seenAtt[id] = struct{}{}
	}
	if n.ConcurrencyRegistration != nil {
		if err := n.ConcurrencyRegistration.Validate(); err != nil {
			return fmt.Errorf("lipruntime: concurrency_registration: %w", err)
		}
	}
	seenRater := make(map[string]struct{}, len(n.RaterRegistrations))
	for i, reg := range n.RaterRegistrations {
		if err := reg.Validate(); err != nil {
			return fmt.Errorf("lipruntime: rater_registrations[%d]: %w", i, err)
		}
		id := strings.TrimSpace(reg.ID)
		if _, dup := seenRater[id]; dup {
			return fmt.Errorf("lipruntime: duplicate rater registration id %q", id)
		}
		seenRater[id] = struct{}{}
	}
	return rejectOverlappingRegistrationStages(n)
}

func rejectOverlappingRegistrationStages(n normalizedProduction) error {
	stagesByID := map[string]map[authority.Stage]struct{}{}
	claim := func(id string, postures []authority.StagePosture) error {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil
		}
		seen := stagesByID[id]
		if seen == nil {
			seen = map[authority.Stage]struct{}{}
			stagesByID[id] = seen
		}
		for _, p := range postures {
			if _, dup := seen[p.Stage]; dup {
				return fmt.Errorf("lipruntime: duplicate provider id %q for overlapping stage %q", id, p.Stage)
			}
			seen[p.Stage] = struct{}{}
		}
		return nil
	}
	for _, reg := range n.RequestRegistrations {
		if err := claim(reg.Descriptor.ID, reg.Descriptor.Postures); err != nil {
			return err
		}
	}
	for _, reg := range n.AttemptRegistrations {
		if err := claim(reg.Descriptor.ID, reg.Descriptor.Postures); err != nil {
			return err
		}
	}
	if n.ConcurrencyRegistration != nil {
		if err := claim(n.ConcurrencyRegistration.Descriptor.ID, n.ConcurrencyRegistration.Descriptor.Postures); err != nil {
			return err
		}
	}
	return nil
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
		if d.EffectiveKind() == authority.ProviderKindObserver {
			continue
		}
		if descriptorHasFamily(d, family) {
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
	if len(providers) == 0 {
		return nil, nil
	}
	if len(descs) != len(providers) {
		return nil, fmt.Errorf("lipruntime: legacy RequestProviders require matching request-stage ProviderDescriptors (got %d providers, %d descriptors)", len(providers), len(descs))
	}
	out := make([]authority.RequestRegistration, 0, len(providers))
	for i, p := range providers {
		if p == nil {
			return nil, fmt.Errorf("lipruntime: RequestProviders[%d]: nil provider", i)
		}
		out = append(out, authority.RequestRegistration{
			Descriptor: descs[i],
			Priority:   authority.RequestPriorityQuotaBudgetRate,
			Provider:   p,
		})
	}
	return out, nil
}

func legacyAttemptRegistrations(providers []authority.AttemptProvider, descs []authority.ProviderDescriptor) ([]authority.AttemptRegistration, error) {
	if len(providers) == 0 {
		return nil, nil
	}
	if len(descs) != len(providers) {
		return nil, fmt.Errorf("lipruntime: legacy AttemptProviders require matching attempt-stage ProviderDescriptors (got %d providers, %d descriptors)", len(providers), len(descs))
	}
	out := make([]authority.AttemptRegistration, 0, len(providers))
	for i, p := range providers {
		if p == nil {
			return nil, fmt.Errorf("lipruntime: AttemptProviders[%d]: nil provider", i)
		}
		out = append(out, authority.AttemptRegistration{
			Descriptor: descs[i],
			Priority:   authority.AttemptPriorityHardSpend,
			Provider:   p,
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
