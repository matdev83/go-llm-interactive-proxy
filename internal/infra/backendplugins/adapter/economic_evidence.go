package adapter

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

// economicEvidenceBuffer keeps host-only V2 observations source-separated and
// replay-safe while a managed attempt is being drained. A frame and a later
// finalizer report with the same source identity/fingerprint are one provider
// charge; a conflicting payload is retained and terminates the attempt.
type economicEvidenceBuffer struct {
	items      []backendplugin.AccountingEvidenceV2
	byIdentity map[string]map[string]struct{}
}

func (b *economicEvidenceBuffer) append(e backendplugin.AccountingEvidenceV2) error {
	if err := e.Validate(); err != nil {
		return err
	}
	// The frame owner may reuse or mutate its nested slices after the callback
	// returns. Retain an immutable copy before it enters the attempt buffer.
	e.Observation = e.Observation.Clone()
	if e.Coverage == "" {
		e.Coverage = backendplugin.EvidenceCoverageComplete
	}
	identity := e.Observation.IdentityKey()
	fingerprint := economicEvidenceFingerprint(e)
	if identity == "" || fingerprint == "" {
		return fmt.Errorf("%w: invalid observation identity", backendplugin.ErrInvalidFrame)
	}
	if b.byIdentity == nil {
		b.byIdentity = make(map[string]map[string]struct{})
	}
	fingerprints := b.byIdentity[identity]
	if fingerprints == nil {
		fingerprints = make(map[string]struct{})
		b.byIdentity[identity] = fingerprints
	}
	if _, ok := fingerprints[fingerprint]; ok {
		if len(fingerprints) > 1 {
			return backendplugin.ErrAccountingEvidenceV2Conflict
		}
		return nil
	}
	if len(fingerprints) != 0 {
		if len(b.items) >= int(backendplugin.DefaultMaxEconomicEvidencePerAttempt) {
			return backendplugin.ErrOversizedMessage
		}
		// Retain both records so reconciliation can surface the conflict. The
		// caller receives a typed failure and must not rate either as complete.
		b.items = append(b.items, e)
		fingerprints[fingerprint] = struct{}{}
		return backendplugin.ErrAccountingEvidenceV2Conflict
	}
	if len(b.items) >= int(backendplugin.DefaultMaxEconomicEvidencePerAttempt) {
		return backendplugin.ErrOversizedMessage
	}
	fingerprints[fingerprint] = struct{}{}
	b.items = append(b.items, e)
	return nil
}

func economicEvidenceFingerprint(e backendplugin.AccountingEvidenceV2) string {
	return e.Observation.Fingerprint() + "\x00" + string(e.Coverage) + "\x00" + e.CoverageReason
}

func (b *economicEvidenceBuffer) drain() []backendplugin.AccountingEvidenceV2 {
	if b == nil || len(b.items) == 0 {
		return nil
	}
	out := make([]backendplugin.AccountingEvidenceV2, len(b.items))
	for i := range b.items {
		out[i] = backendplugin.AccountingEvidenceV2{
			Observation:    b.items[i].Observation.Clone(),
			Coverage:       b.items[i].Coverage,
			CoverageReason: b.items[i].CoverageReason,
		}
	}
	b.items = nil
	b.byIdentity = nil
	return out
}
