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
)
