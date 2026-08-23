package reasoningpreservation

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
)

// PollForAttemptKind is a typed outcome for the one-shot poll attempt.
type PollForAttemptKind string

const (
	PollKindNoPending   PollForAttemptKind = "no_pending"
	PollKindUnavailable PollForAttemptKind = "unavailable"
	PollKindPending     PollForAttemptKind = "pending"
	PollKindFailed      PollForAttemptKind = "failed"
	PollKindNotFound    PollForAttemptKind = "not_found"
	PollKindCompleted   PollForAttemptKind = "completed"
	PollKindPollError   PollForAttemptKind = "poll_error"
)

// CompletedPollCandidate is a typed completed adoption candidate for the next
// stage (5.2). In 5.1 the candidate is produced but original is still replayed
// (shadow). It carries the defensive Collected copy plus correlation IDs.
type CompletedPollCandidate struct {
	Partition     SessionPartition
	ArtifactID    string
	ReservationID string
	JobID         auxiliary.JobID
	Collected     lipapi.Collected
	PollState     auxiliary.PollState
}

// CompressionPollAttemptResult is the typed result of the one-shot poll.
type CompressionPollAttemptResult struct {
	Kind      PollForAttemptKind
	Candidate *CompletedPollCandidate
	State     auxiliary.PollState
	Err       error
}

// PollOnceForMatchingArtifact performs a single non-blocking Poll for the first
// restorable artifact (ClassMissing and destination-supported) that has a pending
// bound JobID. It shares ClassifyAssistantTurns / dialectSet semantics with
// RestoreMissingReasoning via collectRestoreCandidates. Never calls Await or busy-waits;
// operational errors are compression-local and must NOT trigger authoritative
// on_state_error reject.
func PollOnceForMatchingArtifact(ctx context.Context, call *lipapi.Call, cs CompressionStore, partition SessionPartition, artifacts []TurnArtifact, support lipapi.ReasoningReplaySupport, svc CompressionServices) CompressionPollAttemptResult {
	_, candidates, err := collectRestoreCandidates(call, artifacts, support)
	if err != nil {
		// Classification/validation errors are poll-local; fall back to direct scan if call is nil for test helper
		if call == nil {
			return pollOnceDirectScan(ctx, cs, partition, artifacts, svc)
		}
		return CompressionPollAttemptResult{Kind: PollKindPollError, Err: err}
	}
	// Filter to supported only for poll
	var supported []restoreCandidate
	for _, c := range candidates {
		if !c.Unsupported {
			supported = append(supported, c)
		}
	}
	if len(supported) == 0 && call != nil {
		return CompressionPollAttemptResult{Kind: PollKindNoPending}
	}
	if len(supported) == 0 && call == nil {
		return pollOnceDirectScan(ctx, cs, partition, artifacts, svc)
	}
	return pollOnceWithCandidates(ctx, cs, partition, supported, svc)
}

// pollOnceWithCandidates polls the first candidate with pending JobID without reclassifying.
func pollOnceWithCandidates(ctx context.Context, cs CompressionStore, partition SessionPartition, candidates []restoreCandidate, svc CompressionServices) CompressionPollAttemptResult {
	if cs == nil {
		return CompressionPollAttemptResult{Kind: PollKindUnavailable}
	}
	if isNilCapability(svc.Poller) {
		return CompressionPollAttemptResult{Kind: PollKindUnavailable}
	}
	if len(candidates) == 0 {
		return CompressionPollAttemptResult{Kind: PollKindNoPending}
	}
	var target restoreCandidate
	var targetState CompressionState
	var found bool
	for _, c := range candidates {
		st, ok, gerr := cs.GetCompressionState(ctx, partition, c.ArtifactID)
		if gerr != nil {
			return CompressionPollAttemptResult{Kind: PollKindPollError, Err: gerr}
		}
		if !ok || st.Pending == nil || string(st.Pending.JobID) == "" {
			continue
		}
		target = c
		targetState = st
		found = true
		break
	}
	if !found {
		return CompressionPollAttemptResult{Kind: PollKindNoPending}
	}
	return pollOnceForTarget(ctx, cs, partition, target, targetState, svc)
}

func pollOnceDirectScan(ctx context.Context, cs CompressionStore, partition SessionPartition, artifacts []TurnArtifact, svc CompressionServices) CompressionPollAttemptResult {
	if cs == nil {
		return CompressionPollAttemptResult{Kind: PollKindUnavailable}
	}
	if isNilCapability(svc.Poller) {
		return CompressionPollAttemptResult{Kind: PollKindUnavailable}
	}
	for i := range artifacts {
		id := artifacts[i].ID
		st, ok, gerr := cs.GetCompressionState(ctx, partition, id)
		if gerr != nil {
			return CompressionPollAttemptResult{Kind: PollKindPollError, Err: gerr}
		}
		if !ok || st.Pending == nil || string(st.Pending.JobID) == "" {
			continue
		}
		return pollOnceForTarget(ctx, cs, partition, restoreCandidate{Artifact: artifacts[i], ArtifactID: id}, CompressionState{ReservationID: st.ReservationID, Pending: st.Pending}, svc)
	}
	return CompressionPollAttemptResult{Kind: PollKindNoPending}
}

func pollOnceForTarget(ctx context.Context, cs CompressionStore, partition SessionPartition, target restoreCandidate, targetState CompressionState, svc CompressionServices) CompressionPollAttemptResult {
	targetID := target.ArtifactID
	reservationID := targetState.ReservationID
	jobID := targetState.Pending.JobID
	pr, err := svc.Poller.Poll(ctx, jobID)
	if err != nil {
		return CompressionPollAttemptResult{Kind: PollKindPollError, Err: err}
	}
	switch pr.State {
	case auxiliary.PollPending:
		return CompressionPollAttemptResult{Kind: PollKindPending, State: pr.State}
	case auxiliary.PollFailed:
		_ = cs.ClearCompression(ctx, partition, targetID, reservationID)
		if !isNilCapability(svc.Client) {
			svc.Client.Forget(jobID)
		}
		return CompressionPollAttemptResult{Kind: PollKindFailed, State: pr.State, Err: pr.Err}
	case auxiliary.PollNotFound:
		_ = cs.ClearCompression(ctx, partition, targetID, reservationID)
		if !isNilCapability(svc.Client) {
			svc.Client.Forget(jobID)
		}
		return CompressionPollAttemptResult{Kind: PollKindNotFound, State: pr.State}
	case auxiliary.PollCompleted:
		// Defensive clone of mutable Collected (strings.Builder, maps, slices)
		cloned := cloneCollected(pr.Collected)
		cand := &CompletedPollCandidate{
			Partition:     partition,
			ArtifactID:    targetID,
			ReservationID: reservationID,
			JobID:         jobID,
			Collected:     cloned,
			PollState:     pr.State,
		}
		return CompressionPollAttemptResult{Kind: PollKindCompleted, Candidate: cand, State: pr.State}
	default:
		return CompressionPollAttemptResult{Kind: PollKindPollError, Err: fmt.Errorf("unknown poll state %d", pr.State), State: pr.State}
	}
}

// cloneCollected defensively copies a Collected to avoid shallow Builder sharing.
func cloneCollected(in lipapi.Collected) lipapi.Collected {
	var out lipapi.Collected
	out.Text.WriteString(in.Text.String())
	out.Reasoning.WriteString(in.Reasoning.String())
	if len(in.ToolArgs) > 0 {
		out.ToolArgs = make(map[string]*strings.Builder, len(in.ToolArgs))
		for k, v := range in.ToolArgs {
			if v != nil {
				b := &strings.Builder{}
				b.WriteString(v.String())
				out.ToolArgs[k] = b
			}
		}
	}
	if len(in.ToolNames) > 0 {
		out.ToolNames = make(map[string]string, len(in.ToolNames))
		for k, v := range in.ToolNames {
			out.ToolNames[k] = v
		}
	}
	if len(in.ToolCallOrder) > 0 {
		out.ToolCallOrder = append([]string(nil), in.ToolCallOrder...)
	}
	if len(in.Warnings) > 0 {
		out.Warnings = append([]string(nil), in.Warnings...)
	}
	out.InputTokens = in.InputTokens
	out.OutputTokens = in.OutputTokens
	out.CacheReadTokens = in.CacheReadTokens
	out.CacheWriteTokens = in.CacheWriteTokens
	out.ReasoningTokens = in.ReasoningTokens
	out.TotalTokens = in.TotalTokens
	out.CostNanoUnits = in.CostNanoUnits
	out.Currency = in.Currency
	out.CostSource = in.CostSource
	out.FinishReceived = in.FinishReceived
	out.FinishReason = in.FinishReason
	if in.TerminalError != nil {
		out.TerminalError = cloneEvent(in.TerminalError)
	}
	if len(in.AssistantMedia) > 0 {
		out.AssistantMedia = make([]lipapi.Part, len(in.AssistantMedia))
		for i := range in.AssistantMedia {
			out.AssistantMedia[i] = clonePart(in.AssistantMedia[i])
		}
	}
	if len(in.ReasoningParts) > 0 {
		out.ReasoningParts = make([]lipapi.ReasoningPart, len(in.ReasoningParts))
		for i := range in.ReasoningParts {
			rp := in.ReasoningParts[i]
			clonedOpaque := rp.Opaque
			if len(rp.Opaque) > 0 {
				clonedOpaque = append([]byte(nil), rp.Opaque...)
			}
			clonedSummary := rp.Summary
			if len(rp.Summary) > 0 {
				clonedSummary = append([]byte(nil), rp.Summary...)
			}
			clonedContent := rp.Content
			if len(rp.Content) > 0 {
				clonedContent = append([]byte(nil), rp.Content...)
			}
			clonedEncrypted := rp.EncryptedContent
			if len(rp.EncryptedContent) > 0 {
				clonedEncrypted = append([]byte(nil), rp.EncryptedContent...)
			}
			out.ReasoningParts[i] = lipapi.ReasoningPart{
				Dialect:                 rp.Dialect,
				Text:                    rp.Text,
				Signature:               rp.Signature,
				Opaque:                  clonedOpaque,
				Summary:                 clonedSummary,
				SummaryPresent:          rp.SummaryPresent,
				Content:                 clonedContent,
				ContentPresent:          rp.ContentPresent,
				EncryptedContent:        clonedEncrypted,
				EncryptedContentPresent: rp.EncryptedContentPresent,
			}
		}
	}
	return out
}

func cloneRaw(in json.RawMessage) json.RawMessage {
	if in == nil {
		return nil
	}
	return append(json.RawMessage(nil), in...)
}

func cloneBytes(in []byte) []byte {
	if in == nil {
		return nil
	}
	return append([]byte(nil), in...)
}

func cloneEvent(in *lipapi.Event) *lipapi.Event {
	if in == nil {
		return nil
	}
	out := *in
	out.Opaque = cloneBytes(in.Opaque)
	out.Reasoning = cloneReasoningPartDeep(in.Reasoning)
	out.Item = cloneItemDeep(in.Item)
	if in.UsageScopes != nil {
		out.UsageScopes = slices.Clone(in.UsageScopes)
	}
	return &out
}

func cloneReasoningPartDeep(in *lipapi.ReasoningPart) *lipapi.ReasoningPart {
	if in == nil {
		return nil
	}
	out := *in
	out.Opaque = cloneRaw(in.Opaque)
	out.Summary = cloneRaw(in.Summary)
	out.Content = cloneRaw(in.Content)
	out.EncryptedContent = cloneRaw(in.EncryptedContent)
	return &out
}

func cloneItemDeep(in *lipapi.Item) *lipapi.Item {
	if in == nil {
		return nil
	}
	out := *in
	if in.Content != nil {
		out.Content = make([]lipapi.ContentPart, len(in.Content))
		for i := range in.Content {
			out.Content[i] = cloneContentPartDeep(in.Content[i])
		}
	}
	if in.Reference != nil {
		ref := *in.Reference
		out.Reference = &ref
	}
	if in.ToolCall != nil {
		tc := *in.ToolCall
		tc.Arguments = cloneRaw(in.ToolCall.Arguments)
		out.ToolCall = &tc
	}
	if in.ToolResult != nil {
		tr := *in.ToolResult
		if in.ToolResult.Parts != nil {
			tr.Parts = make([]lipapi.ContentPart, len(in.ToolResult.Parts))
			for i := range in.ToolResult.Parts {
				tr.Parts[i] = cloneContentPartDeep(in.ToolResult.Parts[i])
			}
		}
		out.ToolResult = &tr
	}
	if in.Reasoning != nil {
		ri := *in.Reasoning
		ri.Reasoning = cloneReasoningPartDeep(in.Reasoning.Reasoning)
		out.Reasoning = &ri
	}
	if in.Compaction != nil {
		ci := *in.Compaction
		ci.Opaque = cloneRaw(in.Compaction.Opaque)
		out.Compaction = &ci
	}
	if in.Extension != nil {
		ext := *in.Extension
		ext.Data = cloneRaw(in.Extension.Data)
		out.Extension = &ext
	}
	return &out
}

func cloneContentPartDeep(in lipapi.ContentPart) lipapi.ContentPart {
	out := in
	out.Reasoning = cloneReasoningPartDeep(in.Reasoning)
	if in.Annotation != nil {
		ann := *in.Annotation
		ann.Data = cloneRaw(in.Annotation.Data)
		out.Annotation = &ann
	}
	if in.Extension != nil {
		ext := *in.Extension
		ext.Data = cloneRaw(in.Extension.Data)
		out.Extension = &ext
	}
	return out
}

// handleCompletedPollCandidate is the 5.2 placeholder. In 5.1 it returns call
// unchanged (shadow). 5.2 will extend to apply raw guard / surrogate without repoll.
// Capturing result locally avoids cross-attempt mutable state.
func handleCompletedPollCandidate(_ context.Context, res CompressionPollAttemptResult, call *lipapi.Call) *lipapi.Call {
	if res.Kind != PollKindCompleted || res.Candidate == nil {
		return call
	}
	// Shadow: do not mutate call; candidate is typed and defensively cloned for next stage.
	return call
}
