package domain

import "errors"

// Domain errors for secure-session policy and storage.
// The composition root sets runtime.Executor.SessionDenialMapper (e.g. lipapidenial.MapToSessionDenial) to
// translate these to lipapi session denials for clients; domain and app return these sentinels unchanged.
var (
	ErrSessionNotFound       = errors.New("securesession: session not found")
	ErrDuplicateSessionID    = errors.New("securesession: duplicate session id")
	ErrDuplicateFingerprint  = errors.New("securesession: duplicate resume fingerprint")
	ErrInvalidResumeToken    = errors.New("securesession: invalid resume token")
	ErrOwnerMismatch         = errors.New("securesession: owner mismatch")
	ErrResumeExpired         = errors.New("securesession: resume window expired")
	ErrWorkspaceDenied       = errors.New("securesession: workspace denied")
	ErrPolicyUnavailable     = errors.New("securesession: policy unavailable")
	ErrStorageUnavailable    = errors.New("securesession: storage unavailable")
	ErrMandatoryAuditFailure = errors.New("securesession: mandatory audit failure")
	ErrMissingPrincipal      = errors.New("securesession: missing principal")
	ErrTranscriptDisabled    = errors.New("securesession: transcript capture disabled for session")
	// ErrWorkspaceUnresolved is returned when workspace resolution failed under fail-closed policy
	// (Req 11.6: do not fail open into an ambiguous empty workspace for secure-session turns).
	ErrWorkspaceUnresolved = errors.New("securesession: workspace could not be resolved")
)
