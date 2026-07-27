package adapter

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/diagredact"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func capsToLipapi(c backendplugin.CapabilitySummary) lipapi.BackendCaps {
	var list []lipapi.Capability
	if c.Streaming {
		list = append(list, lipapi.CapabilityStreaming)
	}
	if c.Tools {
		list = append(list, lipapi.CapabilityTools)
	}
	if c.Vision {
		list = append(list, lipapi.CapabilityVision)
	}
	if c.Documents {
		list = append(list, lipapi.CapabilityDocuments)
	}
	if c.StructuredOutputs {
		list = append(list, lipapi.CapabilityStructuredOutputs)
	}
	if c.Reasoning {
		list = append(list, lipapi.CapabilityReasoning)
	}
	if c.ReasoningReplay {
		list = append(list, lipapi.CapabilityReasoningReplay)
	}
	if c.ParallelToolCalls {
		list = append(list, lipapi.CapabilityParallelToolCalls)
	}
	return lipapi.NewBackendCaps(list...)
}

func eventToLipapi(ev *backendplugin.CanonicalEvent) (lipapi.Event, error) {
	if ev == nil {
		return lipapi.Event{}, fmt.Errorf("nil event")
	}
	if err := ev.Kind.Validate(); err != nil {
		return lipapi.Event{}, err
	}
	out := lipapi.Event{Kind: lipapi.EventKind(ev.Kind)}
	if ev.MessageIndex != nil {
		out.MessageIndex = int(*ev.MessageIndex)
	}
	if ev.Delta != nil {
		out.Delta = *ev.Delta
	}
	if ev.Signature != nil {
		out.Signature = *ev.Signature
	}
	if len(ev.Opaque) > 0 {
		out.Opaque = append([]byte(nil), ev.Opaque...)
	}
	if ev.ToolCallID != nil {
		out.ToolCallID = *ev.ToolCallID
	}
	if ev.ToolName != nil {
		out.ToolName = *ev.ToolName
	}
	if ev.Usage != nil {
		if ev.Usage.Presence.InputTokens && ev.Usage.InputTokens != nil {
			out.InputTokens = int(*ev.Usage.InputTokens)
		}
		if ev.Usage.Presence.OutputTokens && ev.Usage.OutputTokens != nil {
			out.OutputTokens = int(*ev.Usage.OutputTokens)
		}
	}
	if ev.Warning != nil {
		out.WarningMessage = sanitizeDiagnosticText(*ev.Warning, 256)
	}
	if ev.Error != nil {
		out.ErrorCode = truncate(string(ev.Error.Code), 64)
		out.ErrorMessage = sanitizeDiagnosticText(ev.Error.Message, 256)
	}
	if ev.ImageRef != nil {
		out.AssistantRef = *ev.ImageRef
	}
	if ev.FileRef != nil {
		out.AssistantRef = *ev.FileRef
	}
	if err := lipapi.ValidateEventEnvelope(&out); err != nil {
		return lipapi.Event{}, err
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ClassifiedError is a sanitized plugin/transport error for core ownership.
type ClassifiedError struct {
	Code            string
	Message         string
	Retryable       bool
	OutputCommitted bool
}

func (e *ClassifiedError) Error() string {
	if e == nil {
		return "plugin error"
	}
	return e.Code + ": " + e.Message
}

// Unwrap exposes lipapi.ErrRecoverablePreOutput for pre-output retryable failures
// so the existing executor seam can swallow/failover without importing adapter.
func (e *ClassifiedError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Retryable && !e.OutputCommitted {
		return lipapi.ErrRecoverablePreOutput
	}
	return nil
}

func sanitizePluginError(e *backendplugin.PluginError) *ClassifiedError {
	if e == nil {
		return nil
	}
	return &ClassifiedError{
		Code:            string(e.Code),
		Message:         sanitizeDiagnosticText(e.Message, 256),
		Retryable:       e.Retryable,
		OutputCommitted: e.OutputCommitted,
	}
}

func sanitizeDiagnosticText(msg string, n int) string {
	return diagredact.Sanitize(msg, n)
}
