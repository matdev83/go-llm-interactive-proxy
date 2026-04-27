package auth

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"

// DeviceIdentity distinguishes app, device, or key material from a human principal.
// Fingerprints and IDs must be non-secret (or redacted) when emitted to events.
type DeviceIdentity struct {
	ID          string
	KeyID       string
	Fingerprint string
}

// Challenge carries non-secret information about an auth challenge (e.g. SSO not satisfied
// even when a device or API key was recognized).
type Challenge struct {
	Kind       ChallengeKind
	ReasonCode string
	// Summary is a short operator-safe, non-secret message (e.g. "SSO sign-in required").
	Summary string
}

// Decision is a single auth result for an inbound call.
type Decision struct {
	Outcome   DecisionOutcome
	Principal execview.PrincipalView
	Device    DeviceIdentity
	// SatisfiedLevel is the auth level the decision satisfied, if any, relative to policy.
	SatisfiedLevel RequiredLevel
	Challenge      Challenge
	// ReasonCode is a stable, machine-oriented reason (e.g. "invalid_api_key", "remote_denied").
	ReasonCode string
}
