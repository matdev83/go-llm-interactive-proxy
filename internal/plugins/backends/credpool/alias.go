// Package credpool re-exports the public credential pool from [pkg/credpool].
package credpool

import pkg "github.com/matdev83/go-llm-interactive-proxy/pkg/credpool"

type (
	Credential       = pkg.Credential
	State            = pkg.State
	CredentialStatus = pkg.CredentialStatus
	Pool             = pkg.Pool
)

const (
	StateUsable      = pkg.StateUsable
	StateCooldown    = pkg.StateCooldown
	StateAuthInvalid = pkg.StateAuthInvalid
)

var (
	ErrNoUsableCredential            = pkg.ErrNoUsableCredential
	New                              = pkg.New
	BackendKeySecrets                = pkg.BackendKeySecrets
	NewPoolFromBackendKeys           = pkg.NewPoolFromBackendKeys
	NewPoolFromCredentials           = pkg.NewPoolFromCredentials
	Secrets                          = pkg.Secrets
	CooldownFromRetryAfter           = pkg.CooldownFromRetryAfter
	CooldownFromRetryAfterOrFallback = pkg.CooldownFromRetryAfterOrFallback
)
