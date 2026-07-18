package app

import (
	"context"
	"errors"
)

// Classified invoke outcomes used by the processor (requirements 8.5, 8.7).
var (
	ErrInvalidPayload    = errors.New("terminalwork: invalid payload")
	ErrProviderTimeout   = errors.New("terminalwork: provider timeout")
	ErrProviderOutage    = errors.New("terminalwork: provider outage")
	ErrAmbiguousCommit   = errors.New("terminalwork: ambiguous commit")
	ErrMissingProvider   = errors.New("terminalwork: missing provider")
	ErrDuplicateProvider = errors.New("terminalwork: duplicate provider")
	ErrNilProvider       = errors.New("terminalwork: nil provider")
	ErrUnsupportedKind   = errors.New("terminalwork: unsupported work kind")
	ErrMalformedProvider = errors.New("terminalwork: malformed provider")
	ErrClaimRenewFailed  = errors.New("terminalwork: claim renew failed")
	ErrNotRunning        = errors.New("terminalwork: processor not running")
	ErrAlreadyStarted    = errors.New("terminalwork: processor already started")
)

// IsPermanent reports whether err must quarantine rather than retry.
// Missing/temporarily unresolvable providers are never permanent (design D9).
func IsPermanent(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrInvalidPayload) ||
		errors.Is(err, ErrUnsupportedKind) ||
		errors.Is(err, ErrMalformedProvider)
}

func errorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrInvalidPayload):
		return "invalid_payload"
	case errors.Is(err, ErrMissingProvider):
		return "missing_provider"
	case errors.Is(err, ErrUnsupportedKind):
		return "unsupported_kind"
	case errors.Is(err, ErrMalformedProvider):
		return "malformed_provider"
	case errors.Is(err, ErrClaimRenewFailed):
		return "claim_renew_failed"
	case errors.Is(err, ErrProviderTimeout), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrProviderOutage):
		return "outage"
	case errors.Is(err, ErrAmbiguousCommit):
		return "ambiguous_commit"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "invoke_error"
	}
}
