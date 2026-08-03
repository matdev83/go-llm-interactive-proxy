package backendplugin

// Protocol and default wire bounds for the v1 ABI.
const (
	// ProtocolMajorV1 is the only major version accepted by this package.
	ProtocolMajorV1 = uint32(1)
	// ProtocolMinorExactReasoningParts adds dialect-tagged opaque reasoning parts
	// to invocation parts and canonical stream events.
	ProtocolMinorExactReasoningParts = uint32(1)
	// ProtocolMinorOrderedItems adds ordered item invocation fields, operation metadata,
	// and explicit protocol requirement DTOs.
	ProtocolMinorOrderedItems = uint32(2)
	// ProtocolMinorExactOpenResponsesFields adds exact OpenAI Responses semantics to
	// the ABI: Invocation.PromptCacheKey, inline FileData, opaque extension content
	// parts, reasoning Summary/Content/EncryptedContent presence, and compaction
	// EncryptedContent on invocation items and canonical stream events.
	ProtocolMinorExactOpenResponsesFields = uint32(3)
	// FeatureExactReasoningParts gates use of the additive v1.1 wire fields.
	FeatureExactReasoningParts = "exact_reasoning_parts"
	// FeatureOrderedItems gates ordered item invocation DTO fields.
	FeatureOrderedItems = "ordered_items"
	// FeatureExactOpenResponsesFields gates the additive v1.3 exact OpenAI
	// Responses wire fields on invocation items, canonical events, and Invocation.
	FeatureExactOpenResponsesFields = "exact_openresponses_fields"

	// DefaultMaxMessageBytes is the default whole-message size ceiling.
	DefaultMaxMessageBytes = uint64(4 << 20)
	// DefaultMaxStreamFrameBytes is the default Execute frame payload ceiling.
	DefaultMaxStreamFrameBytes = uint64(1 << 20)
	// DefaultMaxPendingEvents bounds host/plugin pending-event buffers.
	DefaultMaxPendingEvents = uint64(1024)
	// DefaultMaxRawJSONBytes bounds opaque JSON byte fields.
	DefaultMaxRawJSONBytes = uint64(256 << 10)
	// DefaultMaxConfigYAMLBytes bounds configure ConfigYAML.
	DefaultMaxConfigYAMLBytes = uint64(1 << 20)
	// DefaultMaxDiagnosticBytes bounds diagnostic frame text.
	DefaultMaxDiagnosticBytes = uint64(64 << 10)
	// DefaultMaxModelsPerResponse bounds ListModelsResponse.Models.
	DefaultMaxModelsPerResponse = uint32(10_000)
)

// TransportPolicy encodes host/plugin transport constraints for the ABI.
type TransportPolicy struct {
	// DisableAutomaticRetries must be true; transport retries are prohibited.
	DisableAutomaticRetries bool
	// MaxMessageBytes is the whole-message ceiling.
	MaxMessageBytes uint64
	// MaxStreamFrameBytes is the per-frame ceiling and must be <= MaxMessageBytes.
	MaxStreamFrameBytes uint64
}

// DefaultTransportPolicy returns the required v1 transport policy.
func DefaultTransportPolicy() TransportPolicy {
	return TransportPolicy{
		DisableAutomaticRetries: true,
		MaxMessageBytes:         DefaultMaxMessageBytes,
		MaxStreamFrameBytes:     DefaultMaxStreamFrameBytes,
	}
}

// Validate reports whether the transport policy satisfies ABI requirements.
func (p TransportPolicy) Validate() error {
	if !p.DisableAutomaticRetries {
		return ErrTransportRetriesRequiredDisabled
	}
	if p.MaxMessageBytes == 0 || p.MaxStreamFrameBytes == 0 {
		return ErrInvalidBounds
	}
	if p.MaxStreamFrameBytes > p.MaxMessageBytes {
		return ErrInvalidBounds
	}
	return nil
}

// ValidateSize rejects n when it exceeds limit.
func ValidateSize(n, limit uint64) error {
	if limit == 0 {
		return ErrInvalidBounds
	}
	if n > limit {
		return ErrOversizedMessage
	}
	return nil
}

// ValidateRawJSONSize rejects raw JSON payloads over limit.
func ValidateRawJSONSize(n, limit uint64) error {
	if limit == 0 {
		limit = DefaultMaxRawJSONBytes
	}
	if n > limit {
		return ErrOversizedRawJSON
	}
	return nil
}
