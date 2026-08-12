package lipruntime

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
)

// normalizedProduction is the defensively copied descriptor-bound registration set.
type normalizedProduction struct {
	RequestRegistrations    []authority.RequestRegistration
	AttemptRegistrations    []authority.AttemptRegistration
	ConcurrencyRegistration *authority.ConcurrencyRegistration
}

// normalizeCanonicalOptions accepts descriptor-bound registrations only.
func normalizeCanonicalOptions(opts Options) (normalizedProduction, error) {
	out := normalizedProduction{
		RequestRegistrations:    append([]authority.RequestRegistration(nil), opts.RequestRegistrations...),
		AttemptRegistrations:    append([]authority.AttemptRegistration(nil), opts.AttemptRegistrations...),
		ConcurrencyRegistration: opts.ConcurrencyRegistration,
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
