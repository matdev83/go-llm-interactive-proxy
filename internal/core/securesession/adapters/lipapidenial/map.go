// Package lipapidenial maps secure-session domain errors to canonical [pkg/lipapi] session denials.
package lipapidenial

import (
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// MapToSessionDenial maps secure-session domain errors to canonical lipapi session denials.
// Callers classify with errors.As(*lipapi.SessionDenialError) or lipapi.SessionDenialPublicCode.
func MapToSessionDenial(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, domain.ErrMissingPrincipal):
		return lipapi.NewSessionDenialMissingPrincipal("secure_session: missing principal")
	case errors.Is(err, domain.ErrSessionNotFound):
		return lipapi.NewSessionDenialInvalidAuthority("secure_session: session not found")
	case errors.Is(err, domain.ErrInvalidResumeToken):
		return lipapi.NewSessionDenialInvalidAuthority("secure_session: invalid resume token")
	case errors.Is(err, domain.ErrOwnerMismatch):
		return lipapi.NewSessionDenialOwnerMismatch("secure_session: owner mismatch")
	case errors.Is(err, domain.ErrResumeExpired):
		return lipapi.NewSessionDenialResumeExpired("secure_session: resume expired")
	case errors.Is(err, domain.ErrWorkspaceDenied):
		return lipapi.NewSessionDenialWorkspace("secure_session: workspace denied")
	case errors.Is(err, domain.ErrPolicyUnavailable):
		return lipapi.NewSessionDenialPolicyUnavailable("secure_session: policy unavailable")
	case errors.Is(err, domain.ErrWorkspaceUnresolved):
		return lipapi.NewSessionDenialWorkspace("secure_session: workspace could not be resolved")
	case errors.Is(err, domain.ErrStorageUnavailable):
		return lipapi.NewSessionDenialStorageUnavailable("secure_session: storage unavailable")
	case errors.Is(err, domain.ErrMandatoryAuditFailure):
		return lipapi.NewSessionDenialMandatoryAuditFailure("secure_session: mandatory audit failure")
	case errors.Is(err, domain.ErrDuplicateSessionID):
		return lipapi.NewSessionDenialInvalidAuthority("secure_session: duplicate session id")
	case errors.Is(err, domain.ErrDuplicateFingerprint):
		return lipapi.NewSessionDenialInvalidAuthority("secure_session: duplicate resume fingerprint")
	case errors.Is(err, domain.ErrTranscriptDisabled):
		return lipapi.NewSessionDenialInvalidAuthority("secure_session: transcript disabled for session")
	default:
		return lipapi.NewSessionDenialInvalidAuthority("secure_session: unexpected denial")
	}
}
