package configsource

import (
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

// DefaultMaxBytes is the startup-fixed upper bound for one accepted source snapshot.
const DefaultMaxBytes int64 = config.DefaultConfigMaxBytes

// Category aliases the core secret-safe load category so driving adapters and
// typed decoding report one stable vocabulary without reversing dependencies.
type Category = config.LoadCategory

const (
	CategoryOK                = config.CategoryOK
	CategoryMissing           = config.CategoryMissing
	CategoryEmpty             = config.CategoryEmpty
	CategoryWhitespace        = config.CategoryWhitespace
	CategoryOversize          = config.CategoryOversize
	CategoryUnstable          = config.CategoryUnstable
	CategoryNonAtomicUpdate   = config.CategoryNonAtomicUpdate
	CategoryUnsupportedType   = config.CategoryUnsupportedType
	CategoryMalformedYAML     = config.CategoryMalformedYAML
	CategoryMultipleDocuments = config.CategoryMultipleDocuments
	CategoryTrailingContent   = config.CategoryTrailingContent
	CategoryUnknownCoreField  = config.CategoryUnknownCoreField
	CategoryPartialUnreadable = config.CategoryPartialUnreadable
)

// FileIdentity is a platform-stable handle identity used to prove atomic replacement.
type FileIdentity struct {
	Platform string
	Opaque   [32]byte
}

// SourceSnapshot is one accepted bounded read of the fixed startup path.
type SourceSnapshot struct {
	SourceID       string
	HandleIdentity FileIdentity
	Size           int64
	ModTime        time.Time
	PrivateDigest  [32]byte
	Bytes          []byte
	ReadAt         time.Time
}

// ActiveSourceVersion is the last accepted source identity used for no-op /
// non-atomic comparisons (published generation or effective no-op baseline).
type ActiveSourceVersion struct {
	HandleIdentity FileIdentity
	PrivateDigest  [32]byte
}
