package contract

import (
	"net/http"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/ingressdefense"
)

// CredentialDisposition is the closed, conservative answer of the credential
// presence question the ingress self-defense gate asks before it may refuse a
// quarantined source in front of authentication.
//
// The two members carry no credential material, so the answer can never become a
// credential-validity oracle, and the ordering is normative: MayAuthenticate is
// the zero value, so an unprojected probe, an unknown provider and any future
// member all fail open to the normal transport-auth chain.
type CredentialDisposition uint8

const (
	// MayAuthenticate reports that the active provider chain might still
	// authenticate the request as presented, so the request must reach the normal
	// auth chain. It is the safe default and the zero value of the enum.
	MayAuthenticate CredentialDisposition = iota
	// DefinitelyNoCredential reports that the complete active provider chain
	// proves the request carries no credential material it could authenticate
	// with. It is the only license to refuse a quarantined source before
	// authentication.
	DefinitelyNoCredential
)

// CredentialProbe is the conservative credential-presence probe projected per
// generation from the same transport-auth provider slice the auth chain runs.
//
// The gate reads it as "this request definitely cannot authenticate": a
// DefinitelyNoCredential answer licenses a pre-auth refusal, and every other
// answer, including the nil probe, must reach authentication. The probe answers
// a question about the request and never discloses, copies or stores any
// credential.
type CredentialProbe func(r *http.Request) CredentialDisposition

// SelfDefenseObserver receives bounded self-defense observations. Every reason
// is a member of the closed [ingressdefense.AllReasons] vocabulary; no request
// path, source address, prefix text, query, header, User-Agent, credential or
// principal ever reaches it, so it can only produce finite metric labels.
// Observer absence is not authoritative: metrics never decide or change a
// security outcome.
type SelfDefenseObserver interface {
	// Denial records one generic refusal for a finite reason.
	Denial(reason ingressdefense.Reason)
	// QuarantineTransition records one started or escalated quarantine for a
	// finite reason. entryCount is the bounded number of tracked source entries
	// after the transition.
	QuarantineTransition(reason ingressdefense.Reason, entryCount int)
}

// SelfDefenseSecurityInput is the cycle-neutral, non-owning generation
// projection of ingress self-defense into the standard data-plane handler graph.
//
// A nil Policy, or a policy with Enabled false, is the structural fast path: the
// gate wrapper is absent and the auth chain runs unobserved, so a disabled
// generation performs zero self-defense request-side work.
//
// State is a borrowed capability on the process-lifetime adaptive store shared
// by every generation. It is never owned, closed or resized here: a generation
// borrows it and the owning ProcessServices lifetime disposes of it. Resolver is
// the already compiled GeoIP client-address trust configuration, reused verbatim
// so self-defense creates no second forwarding parser and no second trust
// boundary, and it needs no GeoIP country database.
//
// Process-state capacity and inactivity TTL are deliberately absent: they size
// process-owned state, they are restart-required, and no per-request projection
// may carry them.
type SelfDefenseSecurityInput struct {
	// Policy is the immutable, reloadable request policy of this generation.
	Policy *ingressdefense.Policy
	// State is the process-owned bounded adaptive state, borrowed for the
	// lifetime of this generation.
	State *ingressdefense.State
	// Resolver is the shared client-address trust configuration.
	Resolver GeoIPResolverConfig
	// ImpossiblePaths toggles the fixed impossible-path matcher. Disabling it
	// bypasses only the matcher and retains adaptive quarantine behavior.
	ImpossiblePaths bool
	// Probe is the optional conservative credential-presence probe.
	Probe CredentialProbe
	// Observer is the optional bounded metrics seam.
	Observer SelfDefenseObserver
	// Now supplies the request-time instant passed to the adaptive state, so the
	// gate and its paired auth observer share one time base. A nil Now uses
	// time.Now; tests inject a fake clock.
	Now func() time.Time
}

// SelfDefenseEnabled reports whether this projection installs the gate and the
// auth observation. It is the single structural switch both wiring sites use, so
// the disabled posture cannot differ between them.
func (s SelfDefenseSecurityInput) SelfDefenseEnabled() bool {
	return s.Policy != nil && s.Policy.Enabled
}

// CredentialGateProbe returns the gate-side view of the conservative probe: it
// reports whether the request might still authenticate, which is the inverse of
// the gate's refusal rule `!probe(r)`. A nil projection yields a nil probe, and
// the gate treats a nil probe as fail open.
func (s SelfDefenseSecurityInput) CredentialGateProbe() func(r *http.Request) bool {
	if s.Probe == nil {
		return nil
	}
	return func(r *http.Request) bool {
		return s.Probe(r) != DefinitelyNoCredential
	}
}
