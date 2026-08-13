package openresponsescompat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/compatmode"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

// BackendSpec carries the validated construction inputs for one instance.
type BackendSpec struct {
	ID               string
	APIKeyEnvVarRoot string
	APIKey           string
	APIKeys          []string
	BaseURL          string
	HTTPClient       *http.Client
	RequestLimits    RequestLimits
	ResponseLimits   ResponseLimits
	Caps             lipapi.BackendCaps
	DialectSupport   lipapi.DialectSupport
	Inventory        modelinventory.Provider
	// Codec is the explicit codec customization/provenance seam for future
	// provider connectors. The zero value resolves to the generic default via
	// [NewBackend]; provider wrappers may pass explicit options to preserve
	// instance/factory/profile provenance without provider-policy leakage.
	Codec CodecOptions
}

// NewBackend constructs the generic OpenResponses backend for one instance.
// The Open seam maps item-authoritative create calls to a context-aware
// non-streaming JSON request and parses the complete response resource into a
// canonical lifecycle stream.
func NewBackend(spec BackendSpec) execbackend.Backend {
	id := strings.TrimSpace(spec.ID)
	if id == "" {
		return configErrorBackend(id, fmt.Errorf("%s: backend id is required", ID))
	}
	if spec.Caps == nil {
		spec.Caps = lipapi.NewBackendCaps()
	}
	if spec.RequestLimits == (RequestLimits{}) {
		spec.RequestLimits = defaultRequestLimits()
	}
	if spec.ResponseLimits == (ResponseLimits{}) {
		spec.ResponseLimits = defaultResponseLimits()
	}
	if spec.HTTPClient == nil {
		spec.HTTPClient = httpclient.Standard()
	}
	if spec.Codec.profile == "" {
		spec.Codec = DefaultCodecOptions()
	}
	spec.DialectSupport = lipapi.NormalizeDialectSupport(spec.DialectSupport)
	return execbackend.Backend{
		Caps:                    spec.Caps,
		TransportCaps:           OpenResponsesTransportCaps(),
		BackendPrefixes:         []string{id},
		ModelInventory:          spec.Inventory,
		DialectSupport:          spec.DialectSupport,
		EnforcesMaxOutputTokens: true,
		ResolveCaps: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) lipapi.BackendCaps {
			return spec.Caps
		},
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", id, lipapi.ErrNilContext)
			}
			switch call.Invocation.Operation {
			case lipapi.OperationContextCompaction:
				return openCompact(ctx, id, spec, call, cand)
			default:
				return openCreate(ctx, id, spec, call, cand)
			}
		},
	}
}

// acceptsCreateOperation reports whether the generic OpenResponses backend
// serves a create call carrying op. The backend owns the openresponses.create
// operation identity and additionally accepts the legacy message-authority
// create operations produced by the bundled OpenAI frontends so those frontend
// families can route to the OpenResponses backend column. The calls are still
// normalized through the single explicit legacy→ordered-items projector before
// any network work; no pairwise translator exists.
func acceptsCreateOperation(op lipapi.Operation) bool {
	switch op {
	case "", lipapi.OperationOpenResponsesCreate,
		lipapi.OperationOpenAIChatCompletions,
		lipapi.OperationOpenAIResponses:
		return true
	default:
		return false
	}
}

// openCreate is the create mapping for both canonical authority forms. Legacy
// message-authority calls are projected to ordered items through the explicit
// legacy→ordered-items projector before the request builder (Task 5.3). Every
// rejection before the HTTP round trip returns a classified pre-output error
// with zero round trips.
func openCreate(ctx context.Context, id string, spec BackendSpec, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
	if !acceptsCreateOperation(call.Invocation.Operation) {
		return nil, fmt.Errorf("%s: %w: operation %q", id, ErrOperationUnsupported, call.Invocation.Operation)
	}

	if err := call.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	projected, err := normalizeLegacyAuthority(id, spec, call)
	if err != nil {
		return nil, err
	}
	if err := projected.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	if err := checkRequirements(id, projected, spec.Caps, spec.DialectSupport); err != nil {
		return nil, err
	}

	endpointURL, err := resolveCreateEndpoint(spec.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}

	transportMode := call.Invocation.TransportMode
	if transportMode == "" {
		// Legacy message-authority frontends (Anthropic, Gemini) produce empty
		// operations, so transport negotiation leaves the mode unselected. Derive
		// it from the client delivery mode exactly like the bundled backends do,
		// so streaming delivery is honored over SSE.
		transportMode = lipapi.PreferredTransportMode(call.Invocation.DeliveryMode)
	}
	switch transportMode {
	case lipapi.TransportModeStreaming:
		return openCreateStreaming(ctx, id, spec, projected, cand)
	case lipapi.TransportModeNonStreaming, "":
		body, err := buildCreateRequest(id, spec, projected, cand)
		if err != nil {
			return nil, err
		}
		apiKey := compatmode.FirstAPIKey(compatmode.ResolveEnvAPIKeys(spec.APIKeyEnvVarRoot))
		respBody, err := doNonStreaming(ctx, spec.HTTPClient, endpointURL, body, apiKey, spec.ResponseLimits.MaxEventBytes)
		if err != nil {
			return nil, classifyCreateOpenError(fmt.Errorf("%s: %w", id, err))
		}

		events, _, err := parseResource(id, respBody, spec.ResponseLimits)
		if err != nil {
			return nil, classifyCreateOpenError(err)
		}
		return lipapi.NewFixedEventStream(events), nil
	default:
		return nil, fmt.Errorf("%s: %w: transport mode %q is not supported", id, ErrUnrepresentable, call.Invocation.TransportMode)
	}
}

// classifyCreateOpenError classifies a non-streaming create attempt failure for
// failover. Context cancellation/deadline is never retried; terminal upstream
// HTTP failures (4xx validation/auth) stay terminal; every other pre-output
// protocol, transport, content-type, body, or parse failure is retryable so core
// can fail over to another candidate before commitment.
func classifyCreateOpenError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if lipapi.IsRecoverablePreOutput(err) {
		return err
	}
	var hf *httpFailureError
	if errors.As(err, &hf) {
		if hf.Kind == httpFailureAuthInvalid || hf.Kind == httpFailureTerminal {
			return err
		}
	}
	return lipapi.RecoverablePreOutputError(err)
}

// OpenResponsesTransportCaps declares the generic mode's operation+transport
// surface: create over JSON and SSE, and compaction over non-streaming
// transport (the pinned profile's initial compaction transport).
func OpenResponsesTransportCaps() lipapi.BackendTransportCaps {
	return lipapi.NewBackendTransportCaps(
		lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenResponsesCreate,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
		},
		lipapi.OperationTransportSupport{
			Operation: lipapi.OperationContextCompaction,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeNonStreaming},
		},
	)
}

func configErrorBackend(id string, err error) execbackend.Backend {
	return execbackend.Backend{
		Caps:           lipapi.NewBackendCaps(lipapi.CapabilityOrderedItems),
		TransportCaps:  OpenResponsesTransportCaps(),
		ModelInventory: modelinventory.ErrorProvider{Err: err},
		ResolveCaps: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) lipapi.BackendCaps {
			return lipapi.NewBackendCaps(lipapi.CapabilityOrderedItems)
		},
		Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return nil, err
		},
	}
}
