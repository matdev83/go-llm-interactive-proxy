package configsource

import "time"

// DefaultMaxBytes is the startup-fixed upper bound for one accepted source
// snapshot (requirement 2.2 / design Stable Configuration Source). Production
// may expose an override later; the contract suite treats this as the default.
const DefaultMaxBytes int64 = 2 << 20 // 2 MiB

// Category is a bounded, secret-safe source-integrity / decode class used by
// reload status and tests. Values must never embed raw YAML or secrets.
type Category string

const (
	CategoryOK                Category = "ok"
	CategoryMissing           Category = "source_missing"
	CategoryEmpty             Category = "source_empty"
	CategoryWhitespace        Category = "source_whitespace"
	CategoryOversize          Category = "source_oversize"
	CategoryUnstable          Category = "source_unstable"
	CategoryNonAtomicUpdate   Category = "source_non_atomic_update"
	CategoryUnsupportedType   Category = "source_unsupported_type"
	CategoryMalformedYAML     Category = "decode_malformed_yaml"
	CategoryMultipleDocuments Category = "decode_multiple_documents"
	CategoryTrailingContent   Category = "decode_trailing_content"
	CategoryUnknownCoreField  Category = "decode_unknown_core_field"
	CategoryPartialUnreadable Category = "source_partial_unreadable"
)

// FileIdentity is a platform-stable handle identity used to prove atomic
// replacement (requirement 2.9). Opaque is private; never log raw contents.
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

// ActiveSourceVersion is the currently published source identity for no-op /
// non-atomic comparisons (design StableSource.ReadStable).
type ActiveSourceVersion struct {
	HandleIdentity FileIdentity
	PrivateDigest  [32]byte
}
