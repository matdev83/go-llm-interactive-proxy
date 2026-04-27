package runtimebundle

import "errors"

// Sentinel errors for runtime bundle wiring and backend security validation. Use [errors.Is] in tests.
var (
	ErrRemoteDeciderRequired            = errors.New("runtimebundle: effective auth policy requires RemoteDecider but BuildOptions.RemoteDecider is nil")
	ErrAuthEventSinkRequired            = errors.New("runtimebundle: auth.event_delivery is custom but BuildOptions.AuthEventSink is nil")
	ErrAuthEventSinkDisallowed          = errors.New("runtimebundle: BuildOptions.AuthEventSink is set but auth.event_delivery is not custom")
	ErrOAuthUserDisallowedMultiUser     = errors.New("runtimebundle: oauth_user credentials are not allowed when access.mode is multi_user")
	ErrUnknownCredentialMultiUser       = errors.New("runtimebundle: unknown credential mode is not allowed when access.mode is multi_user")
	ErrUnsupportedBackendCredentialMode = errors.New("runtimebundle: unsupported backend credential mode")
)
