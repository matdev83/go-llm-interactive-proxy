package manifest

import "errors"

var (
	ErrInvalidManifest      = errors.New("backendplugin/manifest: invalid manifest")
	ErrUnknownSchema        = errors.New("backendplugin/manifest: unknown schema")
	ErrUnknownField         = errors.New("backendplugin/manifest: unknown field")
	ErrForbiddenField       = errors.New("backendplugin/manifest: forbidden field")
	ErrUnsupportedExtension = errors.New("backendplugin/manifest: unsupported extension")
	ErrBoundsExceeded       = errors.New("backendplugin/manifest: bounds exceeded")
	ErrDuplicateExport      = errors.New("backendplugin/manifest: duplicate export kind")
	ErrInvalidExecutable    = errors.New("backendplugin/manifest: invalid executable path")
	ErrInvalidDigest        = errors.New("backendplugin/manifest: invalid sha256")
	ErrInvalidPlatform      = errors.New("backendplugin/manifest: invalid platform")
	// ErrInconsistentExportSecurityPosture rejects an export whose declared
	// credential mode contradicts its declared access scope. Under the host
	// principal model a user-scoped OAuth credential is never valid in a shared
	// deployment, so credential_mode: oauth_user must declare
	// access_scope: local_only instead of advertising broad eligibility.
	ErrInconsistentExportSecurityPosture = errors.New("backendplugin/manifest: inconsistent export security posture")
)
