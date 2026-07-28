package backendplugin

// UsageEvidence is canonical usage/billing evidence with explicit presence.
type UsageEvidence struct {
	InputTokens      *int64
	OutputTokens     *int64
	CacheReadTokens  *int64
	CacheWriteTokens *int64
	ReasoningTokens  *int64
	TotalTokens      *int64
	Presence         UsagePresence
	RawUsageJSON     RawJSON
}

// CapabilitySummary summarizes canonical backend capabilities.
type CapabilitySummary struct {
	Streaming         bool
	Tools             bool
	Vision            bool
	Documents         bool
	StructuredOutputs bool
	Reasoning         bool
	ReasoningReplay   bool
	ParallelToolCalls bool
}

// TransportCapabilitySummary summarizes transport capabilities.
type TransportCapabilitySummary struct {
	Keepalive           bool
	Cancellation        bool
	BidirectionalStream bool
}

// PluginDescriptor describes a plugin executable and its factory exports.
type PluginDescriptor struct {
	ProtocolMajor uint32
	ProtocolMinor uint32
	PluginID      string
	Version       string
	BuildID       string
	Features      []Feature
	Factories     []FactoryDescriptor
}

// FactoryDescriptor is generic export metadata for one factory kind.
type FactoryDescriptor struct {
	Kind                      string
	DisplayName               string
	Description               string
	CredentialMode            CredentialMode
	AccessScope               AccessScope
	RoutePrefixes             []string
	SupportsCountTokens       bool
	SupportsFinalizeBilling   bool
	SupportsDynamicInventory  bool
	SupportsModelAwareProfile bool
	ProcessSharing            ProcessSharing
	Experimental              bool
	Deprecated                bool
	StaticCapabilities        CapabilitySummary
	TransportCapabilities     TransportCapabilitySummary
}

// SecretBundle carries configure-time secrets (never logged by this package).
type SecretBundle struct {
	Values map[string][]byte
}

// RuntimePolicy is the stable DTO projection of host runtime policy for external
// executable plugins. Hosts must project policy into this type (and its wire
// form in api/backendplugin/v1) rather than sharing *http.Client, internal
// identity config, or mutable registries across the process boundary.
type RuntimePolicy struct {
	MaxRequestBytes         uint64
	MaxStreamFrameBytes     uint64
	MaxPendingEvents        uint64
	RequestTimeoutMS        int64
	CancelDeadlineMS        int64
	DiagnosticsVerbosity    string
	MaxConcurrentExecutions uint32
	LocalOnly               bool
	AllowedEnvNames         []string
	DisableTransportRetries bool
}

// ConfigureRequest configures one backend instance after negotiation and peer auth.
// External plugin configure receives only opaque ConfigYAML, Secrets, RuntimePolicy,
// and negotiation metadata — never *http.Client, internal identity, or a registry.
type ConfigureRequest struct {
	InstanceID       string
	FactoryKind      string
	ConfigYAML       []byte
	Secrets          SecretBundle
	RuntimePolicy    RuntimePolicy
	Negotiation      Negotiation
	NegotiationToken string
}

// ResolvedProfile is the capability/transport profile for an instance or model.
type ResolvedProfile struct {
	Capabilities             CapabilitySummary
	TransportCapabilities    TransportCapabilitySummary
	ReasoningReplaySupported bool
	RoutePrefixes            []string
	EnforceMaxOutput         bool
	MaxOutputTokens          *uint32
	SupportsCountTokens      bool
	SupportsFinalizeBilling  bool
	SupportsDynamicInventory bool
	EvidenceSource           string
	ProfileVersion           string
}

// ModelDescriptor is one inventory row.
type ModelDescriptor struct {
	CanonicalModelID string
	NativeModelID    string
	DisplayName      string
	RoutePrefix      string
	FactoryKind      string
	Capabilities     CapabilitySummary
}

// ListModelsResponse is a bounded inventory result.
type ListModelsResponse struct {
	Models             []ModelDescriptor
	InventorySource    string
	FetchedUnixMS      int64
	RefreshAfterUnixMS *int64
	ErrorCode          string
}

// Invocation is the canonical attempt payload for execute/count.
type Invocation struct {
	RequestID        string
	AttemptID        string
	ALegID           string
	BLegID           string
	CanonicalModelID string
	NativeModelID    string
	Instructions     []Message
	Messages         []Message
	Tools            []ToolDef
	ToolChoice       *string
	Options          GenerationOptions
	SafeMetadata     map[string]string
}

// Message is an ordered role + parts unit.
type Message struct {
	Role  Role
	Parts []Part
}

// Part is one canonical content part with explicit optional fields.
type Part struct {
	Kind          PartKind
	Text          *string
	ImageRef      *string
	FileRef       *string
	ReasoningText *string
	ToolArgsJSON  RawJSON
	ToolCallID    *string
	ToolName      *string
}

// ToolDef describes a tool the model may call.
type ToolDef struct {
	Name           string
	Description    string
	ParametersJSON RawJSON
}

// GenerationOptions carries optional generation controls with presence.
type GenerationOptions struct {
	MaxOutputTokens    *uint32
	TemperatureMillis  *int32
	ReasoningEffort    *string
	ParallelToolCalls  *bool
	ResponseMIMEType   *string
	ResponseSchemaJSON RawJSON
}

// CountTokensRequest asks a plugin to count tokens for an invocation.
type CountTokensRequest struct {
	InstanceID string
	ModelID    string
	Invocation Invocation
}

// CountTokensResponse returns count evidence with presence.
type CountTokensResponse struct {
	InputTokens     *int64
	Presence        UsagePresence
	EvidenceQuality string
}

// FinalizeBillingRequest finalizes billing for an immutable attempt lineage.
type FinalizeBillingRequest struct {
	InstanceID     string
	ALegID         string
	BLegID         string
	ModelID        string
	Reason         string
	IdempotencyKey string
}

// FinalizeBillingResponse returns billing/usage evidence.
type FinalizeBillingResponse struct {
	Usage           UsageEvidence
	EvidenceQuality string
}

// PluginError is a classified plugin error.
type PluginError struct {
	Code            ErrorCode
	Message         string
	Retryable       bool
	OutputCommitted bool
}

// CancelOutcome reports cancellation handling.
type CancelOutcome struct {
	Acknowledged bool
	Detail       string
	Reason       CancelReason
}

// Terminal ends an execute attempt exactly once.
type Terminal struct {
	Status TerminalStatus
	Error  *PluginError
}

// CanonicalEvent is one ordered stream event payload (sequence lives on the frame).
type CanonicalEvent struct {
	Kind         EventKind
	MessageIndex *int32
	Delta        *string
	Signature    *string
	Opaque       []byte
	ToolCallID   *string
	ToolName     *string
	Usage        *UsageEvidence
	Warning      *string
	Error        *PluginError
	ImageRef     *string
	FileRef      *string
}

// ClientFrame is a host-to-plugin Execute frame.
type ClientFrame struct {
	Kind                 ClientFrameKind
	Sequence             uint64
	InstanceID           string
	Invocation           *Invocation
	CancelReason         CancelReason
	CancelDeadlineUnixMS int64
}

// ServerFrame is a plugin-to-host Execute frame.
type ServerFrame struct {
	Kind          ServerFrameKind
	Sequence      uint64
	Event         *CanonicalEvent
	Diagnostic    string
	CancelOutcome *CancelOutcome
	Terminal      *Terminal
}

// HealthResponse reports plugin health.
type HealthResponse struct {
	Serving bool
	Detail  string
}

// GracefulShutdownRequest asks the plugin to drain.
type GracefulShutdownRequest struct {
	DrainTimeoutMS int64
}

// GracefulShutdownResponse acknowledges shutdown.
type GracefulShutdownResponse struct {
	Accepted bool
}
