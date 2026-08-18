package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
)

// RunCompactionPreserverBeforeRequest invokes ordered preservation callbacks
// before the primary request opens. Each callback gets its own bounded clone
// baseline. Callback errors, panics, and invalid mutations roll back only that
// callback and are isolated from primary traffic; later preservers continue.
func RunCompactionPreserverBeforeRequest(
	ctx context.Context,
	log *slog.Logger,
	obs StageMetrics,
	preservers []compaction.Preserver,
	call *lipapi.Call,
	preview compaction.RequestPreview,
	meta compaction.PreservationMeta,
	services compaction.Services,
) error {
	if call == nil {
		return fmt.Errorf("extensions: nil call: %w", lipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return fmt.Errorf("extensions: %w", lipapi.ErrNilContext)
	}
	if execctx.AuxiliaryDepth(ctx) > 0 {
		return nil
	}
	for _, p := range preservers {
		if p == nil {
			continue
		}
		id := safePreserverID(p)
		if execctx.IsSuppressedPluginID(ctx, id) {
			continue
		}
		before := cloneCanonicalCall(*call)
		err := safety.Call(safety.BoundaryExtension, "compaction_preserver_before_request", func() error {
			return p.BeforeRequest(ctx, call, preview, meta, services)
		})
		if err != nil {
			*call = before
			isolatePreserverFailure(ctx, log, obs, id, "before_request", err)
			continue
		}
		if err := call.Validate(); err != nil {
			*call = before
			isolatePreserverFailure(ctx, log, obs, id, "before_request_invalid", err)
		}
	}
	return nil
}

// RunCompactionPreserverRequestOpened invokes preservation callbacks after the
// primary request has opened. The content arguments are deep-copied per
// callback, so callback-local mutation cannot alter primary traffic or another
// callback. Errors and panics are feature-local fail-open outcomes.
func RunCompactionPreserverRequestOpened(
	ctx context.Context,
	log *slog.Logger,
	obs StageMetrics,
	preservers []compaction.Preserver,
	call lipapi.Call,
	events []compaction.Event,
	meta compaction.PreservationMeta,
	services compaction.Services,
) error {
	if ctx == nil {
		return fmt.Errorf("extensions: %w", lipapi.ErrNilContext)
	}
	if execctx.AuxiliaryDepth(ctx) > 0 {
		return nil
	}
	for _, p := range preservers {
		if p == nil {
			continue
		}
		id := safePreserverID(p)
		if execctx.IsSuppressedPluginID(ctx, id) {
			continue
		}
		callbackCall := lipapi.CloneCall(call)
		callbackEvents := cloneCompactionEvents(events)
		err := safety.Call(safety.BoundaryExtension, "compaction_preserver_request_opened", func() error {
			return p.RequestOpened(ctx, callbackCall, callbackEvents, meta, services)
		})
		if err != nil {
			isolatePreserverFailure(ctx, log, obs, id, "request_opened", err)
		}
	}
	return nil
}

// RunCompactionPreserverBeforeResponseRelease invokes ordered final-response
// preservation callbacks. Each callback is transactional against the exact
// pre-callback event. A failed, panicking, or invalid callback is rolled back
// and the committed detector can therefore observe the restored final event.
func RunCompactionPreserverBeforeResponseRelease(
	ctx context.Context,
	log *slog.Logger,
	obs StageMetrics,
	preservers []compaction.Preserver,
	ev *lipapi.Event,
	preview compaction.ResponsePreview,
	meta compaction.PreservationMeta,
	services compaction.Services,
) error {
	if ev == nil {
		return fmt.Errorf("extensions: nil event: %w", lipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return fmt.Errorf("extensions: %w", lipapi.ErrNilContext)
	}
	if execctx.AuxiliaryDepth(ctx) > 0 {
		return nil
	}
	for _, p := range preservers {
		if p == nil {
			continue
		}
		id := safePreserverID(p)
		if execctx.IsSuppressedPluginID(ctx, id) {
			continue
		}
		before := cloneCanonicalEvent(*ev)
		err := safety.Call(safety.BoundaryExtension, "compaction_preserver_before_response_release", func() error {
			return p.BeforeResponseRelease(ctx, ev, preview, meta, services)
		})
		if err != nil {
			*ev = before
			isolatePreserverFailure(ctx, log, obs, id, "before_response_release", err)
			continue
		}
		if err := lipapi.ValidateEventEnvelope(ev); err != nil {
			*ev = before
			isolatePreserverFailure(ctx, log, obs, id, "before_response_release_invalid", err)
		}
	}
	return nil
}

// cloneCompactionEvents clones the public metadata event values. Event has no
// pointer or slice fields, so a value copy is a complete defensive copy.
func cloneCompactionEvents(in []compaction.Event) []compaction.Event {
	if len(in) == 0 {
		return nil
	}
	out := make([]compaction.Event, len(in))
	copy(out, in)
	return out
}

// cloneCanonicalCall builds the canonical deep clone and restores empty
// non-nil slice/map presence where lipapi.CloneCall intentionally normalizes
// zero-length optional carriers. Preservation rollback must retain those
// presence distinctions exactly.
func cloneCanonicalCall(in lipapi.Call) lipapi.Call {
	out := lipapi.CloneCall(in)
	if in.Instructions != nil && out.Instructions == nil {
		out.Instructions = make([]lipapi.Message, 0)
	}
	if in.Messages != nil && out.Messages == nil {
		out.Messages = make([]lipapi.Message, 0)
	}
	if in.Items != nil && out.Items == nil {
		out.Items = make([]lipapi.Item, 0)
	}
	if in.Tools != nil && out.Tools == nil {
		out.Tools = make([]lipapi.ToolDef, 0)
	}
	if in.ToolChoice.AllowedTools != nil && out.ToolChoice.AllowedTools == nil {
		out.ToolChoice.AllowedTools = make([]string, 0)
	}
	if in.Extensions != nil && out.Extensions == nil {
		out.Extensions = make(map[string]json.RawMessage)
	}
	if in.SemanticExtensions != nil && out.SemanticExtensions == nil {
		out.SemanticExtensions = make([]lipapi.SemanticExtension, 0)
	}
	for i := range in.Instructions {
		if i < len(out.Instructions) {
			preserveMessageShape(in.Instructions[i], &out.Instructions[i])
		}
	}
	for i := range in.Messages {
		if i < len(out.Messages) {
			preserveMessageShape(in.Messages[i], &out.Messages[i])
		}
	}
	for i := range in.Items {
		if i < len(out.Items) {
			preserveItemShape(in.Items[i], &out.Items[i])
		}
	}
	for i := range in.Tools {
		if i < len(out.Tools) && in.Tools[i].Parameters != nil && out.Tools[i].Parameters == nil {
			out.Tools[i].Parameters = json.RawMessage{}
		}
	}
	for i := range in.SemanticExtensions {
		if i < len(out.SemanticExtensions) && in.SemanticExtensions[i].Data != nil && out.SemanticExtensions[i].Data == nil {
			out.SemanticExtensions[i].Data = json.RawMessage{}
		}
	}
	return out
}

func preserveMessageShape(in lipapi.Message, out *lipapi.Message) {
	if in.Parts != nil && out.Parts == nil {
		out.Parts = make([]lipapi.Part, 0)
	}
	for i := range in.Parts {
		if i >= len(out.Parts) {
			continue
		}
		if in.Parts[i].Content != nil && out.Parts[i].Content == nil {
			out.Parts[i].Content = json.RawMessage{}
		}
	}
}

func preserveItemShape(in lipapi.Item, out *lipapi.Item) {
	if in.Content != nil && out.Content == nil {
		out.Content = make([]lipapi.ContentPart, 0)
	}
	if in.ToolCall != nil && in.ToolCall.Arguments != nil && out.ToolCall != nil && out.ToolCall.Arguments == nil {
		out.ToolCall.Arguments = json.RawMessage{}
	}
	if in.ToolResult != nil && in.ToolResult.Parts != nil && out.ToolResult != nil && out.ToolResult.Parts == nil {
		out.ToolResult.Parts = make([]lipapi.ContentPart, 0)
	}
	for i := range in.Content {
		if i >= len(out.Content) {
			continue
		}
		preserveContentPartShape(in.Content[i], &out.Content[i])
	}
	if in.ToolResult != nil && out.ToolResult != nil {
		for i := range in.ToolResult.Parts {
			if i < len(out.ToolResult.Parts) {
				preserveContentPartShape(in.ToolResult.Parts[i], &out.ToolResult.Parts[i])
			}
		}
	}
	if in.Compaction != nil && in.Compaction.Opaque != nil && out.Compaction != nil && out.Compaction.Opaque == nil {
		out.Compaction.Opaque = json.RawMessage{}
	}
}

func preserveContentPartShape(in lipapi.ContentPart, out *lipapi.ContentPart) {
	if in.Annotation != nil && in.Annotation.Data != nil && out.Annotation != nil && out.Annotation.Data == nil {
		out.Annotation.Data = json.RawMessage{}
	}
	if in.Extension != nil && in.Extension.Data != nil && out.Extension != nil && out.Extension.Data == nil {
		out.Extension.Data = json.RawMessage{}
	}
}

// cloneCanonicalEvent uses the canonical Call clone implementation for nested
// Item/Reasoning payloads; Event itself is then copied with all nested carriers
// detached from the callback's mutation target.
func cloneCanonicalEvent(in lipapi.Event) lipapi.Event {
	out := in
	if in.Opaque != nil {
		out.Opaque = append([]byte{}, in.Opaque...)
	}
	if in.UsageScopes != nil {
		out.UsageScopes = append([]lipapi.ScopedUsageDelta{}, in.UsageScopes...)
	}
	if in.Reasoning != nil {
		r := *in.Reasoning
		r.Opaque = cloneRawMessage(in.Reasoning.Opaque)
		r.Summary = cloneRawMessage(in.Reasoning.Summary)
		r.Content = cloneRawMessage(in.Reasoning.Content)
		r.EncryptedContent = cloneRawMessage(in.Reasoning.EncryptedContent)
		out.Reasoning = &r
	}
	if in.Item != nil {
		cloned := lipapi.CloneCall(lipapi.Call{Items: []lipapi.Item{*in.Item}}).Items[0]
		preserveItemShape(*in.Item, &cloned)
		out.Item = &cloned
	}
	return out
}

func cloneRawMessage(in json.RawMessage) json.RawMessage {
	if in == nil {
		return nil
	}
	return append(json.RawMessage{}, in...)
}

func safePreserverID(p compaction.Preserver) (id string) {
	if p == nil {
		return ""
	}
	_ = safety.Call(safety.BoundaryExtension, "compaction_preserver_id", func() error {
		id = p.ID()
		return nil
	})
	return id
}

func isolatePreserverFailure(ctx context.Context, log *slog.Logger, obs StageMetrics, id, stage string, err error) {
	if err == nil {
		return
	}
	var pe *safety.PanicError
	if errors.As(err, &pe) {
		if log != nil {
			log.WarnContext(ctx, "compaction preservation callback failed (fail-open)", "preserver", id, "stage", stage, "outcome", "panic")
		}
	} else if log != nil {
		// Do not include callback error text: a feature-local error may contain
		// prompt, response, capsule, or provider content. Diagnostics stay
		// content-free and classify only the failure outcome.
		log.WarnContext(ctx, "compaction preservation callback failed (fail-open)", "preserver", id, "stage", stage, "outcome", "error")
	}
	if obs != nil {
		obs.IncFailOpenSkip(MetricsStageCompactionPreservation)
	}
}
