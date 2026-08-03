package backendplugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

// Mode selects deterministic broken or valid fake plugin behavior.
type Mode string

const (
	ModeValid             Mode = "valid"
	ModeMalformedFrame    Mode = "malformed_frame"
	ModeSlowOutput        Mode = "slow_output"
	ModeBlockedCancel     Mode = "blocked_cancel"
	ModeProcessExit       Mode = "process_exit"
	ModeCommitThenFail    Mode = "commit_then_fail"
	ModeDuplicateTerminal Mode = "duplicate_terminal"
	ModeOversize          Mode = "oversize"
	ModeUnauthorizedPeer  Mode = "unauthorized_peer"
	ModeShutdown          Mode = "shutdown"
	ModeUnknownEventKind  Mode = "unknown_event_kind"
	ModeSecretTerminal    Mode = "secret_terminal"
	ModeExactReasoning    Mode = "exact_reasoning_event"
)

// Stable diagnostic codes for broken modes.
const (
	DiagMalformedFrame    = "fake:malformed_frame"
	DiagSlowOutput        = "fake:slow_output"
	DiagBlockedCancel     = "fake:blocked_cancel"
	DiagProcessExit       = "fake:process_exit"
	DiagCommitThenFail    = "fake:commit_then_fail"
	DiagDuplicateTerminal = "fake:duplicate_terminal"
	DiagOversize          = "fake:oversize"
	DiagUnauthorizedPeer  = "fake:unauthorized_peer"
	DiagShutdown          = "fake:shutdown"
	DiagUnknownEventKind  = "fake:unknown_event_kind"
	DiagSecretTerminal    = "fake:secret_terminal"
	DiagExactReasoning    = "fake:exact_reasoning_event"
)

// FakeService is an in-process deterministic backendplugin.Service for conformance.
type FakeService struct {
	Mode                Mode
	SlowWait            time.Duration
	ExecuteCount        atomic.Int64
	LastStartInvocation *backendplugin.Invocation
	LastStartCall       *lipapi.Call
}

// Describe returns a minimal advertised-capability descriptor.
func (f *FakeService) Describe(ctx context.Context) (backendplugin.PluginDescriptor, error) {
	_ = ctx
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1,
		ProtocolMinor: backendplugin.ProtocolMinorExactOpenResponsesFields,
		PluginID:      "io.golip.fake",
		Version:       "0.0.1",
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureOrderedItems, Required: false},
			{Name: backendplugin.FeatureExactOpenResponsesFields, Required: false},
			{Name: "count_tokens", Required: false},
			{Name: "finalize_billing", Required: false},
		},
		Factories: []backendplugin.FactoryDescriptor{{
			Kind:                     "fake",
			CredentialMode:           backendplugin.CredentialModeNone,
			AccessScope:              backendplugin.AccessScopeLocalOnly,
			ProcessSharing:           backendplugin.ProcessSharingPerInstance,
			SupportsCountTokens:      true,
			SupportsFinalizeBilling:  true,
			SupportsDynamicInventory: true,
			StaticCapabilities: backendplugin.CapabilitySummary{
				Streaming: true, Tools: true, Vision: true, Reasoning: true,
				OrderedItems: true, ItemReferences: true, Compaction: true, OpaqueExtensions: true,
			},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{
				Cancellation: true, BidirectionalStream: true,
			},
		}},
	}, nil
}

// Configure creates a fake instance or fails for unauthorized/shutdown modes.
func (f *FakeService) Configure(ctx context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	_ = ctx
	if f.Mode == ModeUnauthorizedPeer {
		return nil, backendplugin.ModeError{Code: DiagUnauthorizedPeer}
	}
	if f.Mode == ModeShutdown {
		return nil, backendplugin.ModeError{Code: DiagShutdown}
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return &fakeInstance{
		svc:  f,
		mode: f.Mode,
		slow: f.SlowWait,
		id:   req.InstanceID,
		neg:  req.Negotiation,
	}, nil
}

type fakeInstance struct {
	svc  *FakeService
	mode Mode
	slow time.Duration
	id   string
	neg  backendplugin.Negotiation
}

func (f *fakeInstance) Negotiation() backendplugin.Negotiation {
	if f.neg.Compatible {
		return f.neg
	}
	return backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: backendplugin.ProtocolMinorExactOpenResponsesFields,
		EnabledFeatures: []string{backendplugin.FeatureOrderedItems, backendplugin.FeatureExactOpenResponsesFields},
	}
}

func (f *fakeInstance) Resolve(ctx context.Context, modelID *string) (backendplugin.ResolvedProfile, error) {
	_ = ctx
	_ = modelID
	return backendplugin.ResolvedProfile{
		Capabilities: backendplugin.CapabilitySummary{
			Streaming: true, Tools: true, Vision: true, Reasoning: true,
			OrderedItems: true, ItemReferences: true, Compaction: true, OpaqueExtensions: true,
		},
		DialectSupport: backendplugin.DialectSupportDTO{
			ItemDialects: []backendplugin.DialectRequirementDTO{
				{Kind: "item", Dialect: "item_reference"},
			},
			CompactionDialects: []backendplugin.DialectRequirementDTO{
				{Kind: "compaction", Dialect: "compact.v1"},
			},
			ReasoningDialects: []backendplugin.DialectRequirementDTO{
				{Kind: "reasoning", Dialect: string(lipapi.ReasoningDialectOpenAIChatTextV1)},
			},
		},
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsCountTokens:      true,
		SupportsFinalizeBilling:  true,
		SupportsDynamicInventory: true,
		EvidenceSource:           "fake",
		ProfileVersion:           "1",
	}, nil
}

func (f *fakeInstance) ListModels(ctx context.Context, maxModels uint32) (backendplugin.ListModelsResponse, error) {
	_ = ctx
	out := backendplugin.ListModelsResponse{
		Models: []backendplugin.ModelDescriptor{{
			CanonicalModelID: "fake-model",
			NativeModelID:    "fake-model",
			FactoryKind:      "fake",
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		}},
		InventorySource: "fake",
		FetchedUnixMS:   1,
	}
	return out, out.Validate(maxModels)
}

func (f *fakeInstance) Execute(stream backendplugin.ExecuteStream) error {
	if f.mode == ModeProcessExit {
		return backendplugin.ModeError{Code: DiagProcessExit}
	}
	start, err := stream.Recv()
	if err != nil {
		return err
	}
	if start.Kind != backendplugin.ClientFrameStart || start.Invocation == nil {
		return fmt.Errorf("%w: expected start", backendplugin.ErrInvalidFrame)
	}
	if err := backendplugin.ValidateClientFrameBounds(start); err != nil {
		return err
	}
	if f.svc != nil {
		f.svc.ExecuteCount.Add(1)
		invCopy := *start.Invocation
		f.svc.LastStartInvocation = &invCopy
		if call, err := backendplugin.CallFromInvocation(*start.Invocation); err == nil {
			f.svc.LastStartCall = &call
		}
	}
	if err := stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
		return err
	}
	seq := uint64(1)
	switch f.mode {
	case ModeMalformedFrame:
		return backendplugin.ModeError{Code: DiagMalformedFrame}
	case ModeSlowOutput:
		wait := f.slow
		if wait == 0 {
			wait = 20 * time.Millisecond
		}
		select {
		case <-stream.Context().Done():
			return backendplugin.ModeError{Code: DiagSlowOutput}
		case <-time.After(wait):
		}
		return backendplugin.ModeError{Code: DiagSlowOutput}
	case ModeOversize:
		big := make([]byte, 256)
		for i := range big {
			big[i] = 'x'
		}
		ds := string(big)
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameEvent, Sequence: seq,
			Event: &backendplugin.CanonicalEvent{Kind: backendplugin.EventTextDelta, Delta: &ds},
		}); err != nil {
			return err
		}
		return backendplugin.ModeError{Code: DiagOversize}
	case ModeBlockedCancel:
		for {
			f, err := stream.Recv()
			if err != nil {
				// Host/context cancellation must surface as context error (no generation
				// invalidation). ModeError is reserved for blocked-cancel protocol faults.
				if cerr := stream.Context().Err(); cerr != nil {
					return cerr
				}
				if errors.Is(err, io.EOF) {
					return backendplugin.ModeError{Code: DiagBlockedCancel}
				}
				return err
			}
			if f.Kind == backendplugin.ClientFrameCancel {
				return backendplugin.ModeError{Code: DiagBlockedCancel}
			}
		}
	case ModeDuplicateTerminal:
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameTerminal, Sequence: seq,
			Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
		}); err != nil {
			return err
		}
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameTerminal, Sequence: seq + 1,
			Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
		}); err != nil {
			return err
		}
		return backendplugin.ModeError{Code: DiagDuplicateTerminal}
	case ModeCommitThenFail:
		d := "committed"
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameEvent, Sequence: seq,
			Event: &backendplugin.CanonicalEvent{Kind: backendplugin.EventTextDelta, Delta: &d},
		}); err != nil {
			return err
		}
		return backendplugin.ModeError{Code: DiagCommitThenFail}
	case ModeUnknownEventKind:
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameEvent, Sequence: seq,
			Event: &backendplugin.CanonicalEvent{Kind: backendplugin.EventKind("evil_kind")},
		}); err != nil {
			return err
		}
		return backendplugin.ModeError{Code: DiagUnknownEventKind}
	case ModeSecretTerminal:
		return stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameTerminal, Sequence: seq,
			Terminal: &backendplugin.Terminal{
				Status: backendplugin.TerminalFailure,
				Error: &backendplugin.PluginError{
					Code:    backendplugin.ErrorCodeUnavailable,
					Message: `provider rejected api_key=sk-or-v1-openrouter-leak Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig sk-ant-api03-ABCDEFGHIJKLMNOP`,
				},
			},
		})
	case ModeExactReasoning:
		dialect := string(lipapi.ReasoningDialectOpenAIResponsesItemV1)
		ev := &backendplugin.CanonicalEvent{
			Kind:                      backendplugin.EventReasoningPart,
			ReasoningDialect:          &dialect,
			ReasoningSummary:          backendplugin.RawJSONFromBytes([]byte(`[{"type":"summary_text","text":"s"}]`)),
			ReasoningContent:          backendplugin.RawJSONFromBytes([]byte(`[{"type":"output_text","text":"t"}]`)),
			ReasoningEncryptedContent: backendplugin.RawJSONNullValue(),
		}
		if err := stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameEvent, Sequence: seq, Event: ev}); err != nil {
			return err
		}
		return stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameTerminal, Sequence: seq + 1,
			Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
		})
	}

	kinds := []backendplugin.EventKind{
		backendplugin.EventTextDelta,
		backendplugin.EventReasoningDelta,
		backendplugin.EventToolCallStarted,
		backendplugin.EventAssistantImageRef,
		backendplugin.EventUsageDelta,
	}
	for _, kind := range kinds {
		ev := &backendplugin.CanonicalEvent{Kind: kind}
		switch kind {
		case backendplugin.EventTextDelta:
			d := "ok"
			ev.Delta = &d
		case backendplugin.EventReasoningDelta:
			d := "think"
			ev.Delta = &d
		case backendplugin.EventToolCallStarted:
			id := "t1"
			name := "tool"
			ev.ToolCallID = &id
			ev.ToolName = &name
		case backendplugin.EventAssistantImageRef:
			ref := "img://1"
			ev.ImageRef = &ref
		case backendplugin.EventUsageDelta:
			zero := int64(1)
			ev.Usage = &backendplugin.UsageEvidence{
				TotalTokens:  &zero,
				Presence:     backendplugin.UsagePresence{TotalTokens: true},
				RawUsageJSON: backendplugin.RawJSONAbsentValue(),
			}
		}
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameEvent, Sequence: seq, Event: ev,
		}); err != nil {
			return err
		}
		seq++
	}
	return stream.Send(backendplugin.ServerFrame{
		Kind: backendplugin.ServerFrameTerminal, Sequence: seq,
		Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
	})
}

func (f *fakeInstance) Close(ctx context.Context) error {
	_ = ctx
	return nil
}

func (f *fakeInstance) CountTokens(ctx context.Context, req backendplugin.CountTokensRequest) (backendplugin.CountTokensResponse, error) {
	_ = ctx
	n := int64(len(req.Invocation.Messages))
	return backendplugin.CountTokensResponse{
		InputTokens:     &n,
		Presence:        backendplugin.UsagePresence{InputTokens: true},
		EvidenceQuality: "fake",
	}, nil
}

func (f *fakeInstance) FinalizeBilling(ctx context.Context, req backendplugin.FinalizeBillingRequest) (backendplugin.FinalizeBillingResponse, error) {
	_ = ctx
	if err := req.Validate(); err != nil {
		return backendplugin.FinalizeBillingResponse{}, err
	}
	n := int64(1)
	return backendplugin.FinalizeBillingResponse{
		Usage: backendplugin.UsageEvidence{
			TotalTokens:  &n,
			Presence:     backendplugin.UsagePresence{TotalTokens: true},
			RawUsageJSON: backendplugin.RawJSONAbsentValue(),
		},
		EvidenceQuality: "fake",
	}, nil
}

// CommandName is the future executable skeleton name for process-host launch.
const CommandName = "lip-backendplugin-fake"
