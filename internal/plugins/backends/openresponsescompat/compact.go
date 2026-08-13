package openresponsescompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/compatmode"
	proto "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/protocols/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// openCompact maps the protocol-neutral context.compaction operation to the
// pinned remote POST {base}/responses/compact JSON endpoint. Compaction is
// non-streaming only: streaming transport and unsupported semantics are rejected
// before any HTTP round trip. The response is a complete CompactResource parsed
// into a canonical lifecycle/usage/compaction output stream. Every failure
// before the first canonical event is classified so core can fail over before
// commitment; this adapter never retries upstream itself.
func openCompact(ctx context.Context, id string, spec BackendSpec, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
	if call.Invocation.Operation != lipapi.OperationContextCompaction {
		return nil, fmt.Errorf("%s: %w: operation %q", id, ErrOperationUnsupported, call.Invocation.Operation)
	}
	if call.Invocation.TransportMode == lipapi.TransportModeStreaming {
		return nil, fmt.Errorf("%s: %w: streaming transport is not supported for context compaction", id, ErrUnrepresentable)
	}
	if _, ok := spec.Caps[lipapi.CapabilityCompaction]; !ok {
		return nil, fmt.Errorf("%s: %w: the context.compaction operation requires the compaction capability", id, ErrUnrepresentable)
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

	body, err := buildCreateRequestBody(id, spec, projected, cand, false)
	if err != nil {
		return nil, err
	}
	endpointURL, err := resolveCompactEndpoint(spec.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	apiKey := compatmode.FirstAPIKey(compatmode.ResolveEnvAPIKeys(spec.APIKeyEnvVarRoot))
	maxResourceBytes := spec.ResponseLimits.MaxResourceBytes
	if maxResourceBytes <= 0 {
		maxResourceBytes = DefaultMaxResponseResourceBytes
	}
	respBody, err := doNonStreaming(ctx, spec.HTTPClient, endpointURL, body, apiKey, maxResourceBytes)
	if err != nil {
		return nil, classifyCompactOpenError(fmt.Errorf("%s: %w", id, err))
	}

	events, _, err := parseCompactResource(id, respBody, spec.ResponseLimits)
	if err != nil {
		return nil, classifyCompactOpenError(err)
	}
	if len(events) == 2 && events[0].Kind == lipapi.EventResponseStarted && events[1].Kind == lipapi.EventError {
		cause := lipapi.NewStreamError(events[1].ErrorCode, events[1].ErrorMessage)
		return nil, lipapi.RecoverablePreOutputError(fmt.Errorf("%s: upstream compaction failed: %w", id, cause))
	}
	return lipapi.NewFixedEventStream(events), nil
}

// classifyCompactOpenError classifies a compact attempt failure for failover.
// Context cancellation/deadline is never retried; terminal upstream HTTP
// failures (4xx validation/auth) stay terminal; every other pre-output protocol,
// transport, content-type, body, or parse failure is retryable so core can fail
// over to another candidate before commitment.
func classifyCompactOpenError(err error) error {
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

// parseCompactResource parses one complete OpenResponses CompactResource through
// the production codec/state semantics into canonical lifecycle events. It
// rejects trailing data, a wrong object discriminator, unknown output item
// types, unrepresentable output content, and non-terminal statuses. The native
// compact ID, native item IDs, and provider compaction lineage are captured as
// private attempt evidence only.
func parseCompactResource(id string, data []byte, limits ResponseLimits) ([]lipapi.Event, NativeEvidence, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var resource proto.WireCompactResource
	if err := dec.Decode(&resource); err != nil {
		return nil, NativeEvidence{}, fmt.Errorf("%s: %w: %v", id, ErrMalformedResponse, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, NativeEvidence{}, fmt.Errorf("%s: %w: trailing data after compact resource", id, ErrMalformedResponse)
	}
	if strings.TrimSpace(resource.Object) != "response.compaction" {
		return nil, NativeEvidence{}, fmt.Errorf("%s: %w: unexpected object %q in compact resource", id, ErrMalformedResponse, resource.Object)
	}
	if strings.TrimSpace(resource.Status) == "" {
		return nil, NativeEvidence{}, fmt.Errorf("%s: %w: compact resource is missing status", id, ErrMalformedResponse)
	}
	if len(resource.Output) > limits.MaxItems {
		return nil, NativeEvidence{}, fmt.Errorf("%s: %w", id, limitError("compact_items", len(resource.Output), limits.MaxItems))
	}
	return compactResourceToEvents(id, &resource, limits, data)
}

func compactResourceToEvents(id string, resource *proto.WireCompactResource, limits ResponseLimits, raw []byte) ([]lipapi.Event, NativeEvidence, error) {
	native := NativeEvidence{ResponseID: strings.TrimSpace(resource.ID)}
	events := []lipapi.Event{{Kind: lipapi.EventResponseStarted}}

	var textBytes, reasoningBytes int
	for i, w := range resource.Output {
		item, err := proto.DecodeItem(w, proto.DefaultLimits())
		if err != nil {
			return nil, native, fmt.Errorf("%s: %w: compact output[%d]: %v", id, ErrMalformedResponse, i, err)
		}
		native.addItemID(item.ID)
		switch item.Kind {
		case lipapi.ItemKindMessage:
			evs, added, err := messageOutputEvents(id, item, lipapi.MaxContentPartsPerItem)
			if err != nil {
				return nil, native, err
			}
			events = append(events, evs...)
			textBytes += added
			if textBytes > limits.MaxTextBytes {
				return nil, native, fmt.Errorf("%s: %w", id, limitError("compact_text", textBytes, limits.MaxTextBytes))
			}
		case lipapi.ItemKindToolCall:
			evs, added, err := toolCallOutputEvents(id, item)
			if err != nil {
				return nil, native, err
			}
			events = append(events, evs...)
			textBytes += added
			if textBytes > limits.MaxTextBytes {
				return nil, native, fmt.Errorf("%s: %w", id, limitError("compact_text", textBytes, limits.MaxTextBytes))
			}
			if item.ToolCall != nil {
				native.ToolCallIDs = append(native.ToolCallIDs, item.ToolCall.CallID)
			}
		case lipapi.ItemKindReasoning:
			evs, added, err := reasoningOutputEvents(id, item)
			if err != nil {
				return nil, native, err
			}
			events = append(events, evs...)
			reasoningBytes += added
			if reasoningBytes > limits.MaxReasoningBytes {
				return nil, native, fmt.Errorf("%s: %w", id, limitError("compact_reasoning", reasoningBytes, limits.MaxReasoningBytes))
			}
		case lipapi.ItemKindItemReference:
			// Provider-native item references are private attempt evidence and
			// are never forwarded onto the canonical stream; the reusable ordered
			// item window does not require them.
			continue
		case lipapi.ItemKindCompaction:
			// A compaction item is part of the reusable compacted ordered window.
			// Carry it verbatim on the canonical stream so the frontend preserves
			// the canonical compaction trajectory and returns it in
			// CompactResource.output. Provider-native compaction lineage stays
			// private on NativeEvidence.
			itemEvent := lipapi.Event{Kind: lipapi.EventItem, Item: &item}
			if err := lipapi.ValidateEventEnvelope(&itemEvent); err != nil {
				return nil, native, fmt.Errorf("%s: %w: compact output[%d] compaction item is invalid: %v", id, ErrMalformedResponse, i, err)
			}
			events = append(events, itemEvent)
		default:
			return nil, native, fmt.Errorf("%s: %w: compact output[%d] item type %q is not representable in the canonical stream", id, ErrMalformedResponse, i, w.Type)
		}
	}

	if usagePresent(resource.Usage) {
		events = append(events, usageEvent(resource.Usage))
	}

	switch resource.Status {
	case "completed":
		events = append(events, lipapi.Event{Kind: lipapi.EventResponseFinished, ResponseStatus: "completed"})
	case "incomplete":
		events = append(events, lipapi.Event{Kind: lipapi.EventResponseFinished, ResponseStatus: "incomplete", FinishReason: resourceIncompleteReason(raw)})
	case "failed":
		events = append(events, lipapi.Event{Kind: lipapi.EventError, ErrorCode: "upstream_error", ErrorMessage: "upstream reported a compaction failure"})
	default:
		return nil, native, fmt.Errorf("%s: %w: unexpected compact resource status %q", id, ErrMalformedResponse, resource.Status)
	}
	return events, native, nil
}
