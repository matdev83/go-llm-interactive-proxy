package anthropicmessages

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/safecast"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipsdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

// msgStream adapts the Anthropic SSE stream to lipapi.EventStream.
//
// Concurrency: one goroutine calls Recv at a time. Close may run concurrently with
// Recv blocked on sdk.Next; Close closes the SDK stream to unblock Next.
// Context: sdk.Next does not observe ctx; cancel the request context alone may not
// return from Recv until Close runs (see [lipapi.EventStream] cancellation notes).
type msgStream struct {
	mu        sync.Mutex
	closeOnce sync.Once

	sdk *ssestream.Stream[anthropic.MessageStreamEventUnion]

	// backendID prefixes stream-recv errors so failures attribute to the configured
	// backend instance (hosted "anthropic" or a custom-compatible instance prefix).
	backendID           string
	pending             stream.PendingEventQueue
	sawResp             bool
	sawMsg              bool
	terminal            bool
	activeToolID        string
	closed              bool
	providerRequestID   string
	providerServiceTier string
	providerUsage       lipapi.Event
	providerUsageSeen   bool
	providerUsageRaw    string

	// cache, when non-nil, collects foreground cache evidence and issues one
	// renewable observation on the committed terminal. It is wired only for
	// automatic enrollment; nil keeps the stream observation-neutral.
	cache *cacheStreamState
	*coremetering.ProviderEvidenceBuffer
}

// cacheStreamState carries the bounded observation buffer and the plugin-owned
// hook used to issue a renewable target from committed cache evidence.
type cacheStreamState struct {
	hook         CacheObservationHook
	lineage      promptcache.ObservationLineage
	renewal      RenewalSnapshot
	credentialID string
	ttl          string
	evidence     promptcache.CacheEvidence
	observedAt   time.Time
	buffer       promptcache.ObservationBuffer
}

func newMessageStream(s *ssestream.Stream[anthropic.MessageStreamEventUnion], backendID string, maxPending int) lipapi.ManagedEventStream {
	return newMessageStreamWithCache(s, backendID, maxPending, nil)
}

func newMessageStreamWithCache(s *ssestream.Stream[anthropic.MessageStreamEventUnion], backendID string, maxPending int, cache *cacheStreamState) lipapi.ManagedEventStream {
	if s == nil {
		return lipapi.NewFixedEventStream(nil)
	}
	return &msgStream{
		sdk:                    s,
		backendID:              backendID,
		pending:                stream.NewPendingEventQueue(maxPending),
		cache:                  cache,
		ProviderEvidenceBuffer: coremetering.NewProviderEvidenceBuffer(),
	}
}

func (s *msgStream) DrainPromptCacheObservations() []promptcache.Observation {
	if s == nil || s.cache == nil {
		return nil
	}
	return s.cache.buffer.DrainPromptCacheObservations()
}

func (s *msgStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return lipapi.Event{}, err
	}
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return lipapi.Event{}, io.EOF
		}
		if ev, ok := s.pending.PopFront(); ok {
			s.mu.Unlock()
			return ev, nil
		}
		s.mu.Unlock()

		if !s.sdk.Next() {
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				return lipapi.Event{}, io.EOF
			}
			if err := s.sdk.Err(); err != nil {
				s.mu.Unlock()
				return lipapi.Event{}, fmt.Errorf("%s: recv stream: %w", s.backendID, err)
			}
			if s.terminal {
				s.mu.Unlock()
				return lipapi.Event{}, io.EOF
			}
			s.terminal = true
			s.completeCacheObservation()
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseFinished}); err != nil {
				s.mu.Unlock()
				return lipapi.Event{}, err
			}
			s.mu.Unlock()
			continue
		}
		cur := s.sdk.Current()
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			continue
		}
		if err := s.handleEvent(cur); err != nil {
			s.mu.Unlock()
			return lipapi.Event{}, err
		}
		s.mu.Unlock()
	}
}

func (s *msgStream) handleEvent(cur anthropic.MessageStreamEventUnion) error {
	switch v := cur.AsAny().(type) {
	case anthropic.MessageStartEvent:
		if id := strings.TrimSpace(v.Message.ID); id != "" {
			s.providerRequestID = id
		}
		if tier := strings.TrimSpace(string(v.Message.Usage.ServiceTier)); tier != "" {
			s.providerServiceTier = tier
		}
		if !s.sawResp {
			s.sawResp = true
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted}); err != nil {
				return err
			}
		}
		if !s.sawMsg {
			s.sawMsg = true
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted}); err != nil {
				return err
			}
		}
		if u := usageFromMessageStart(v); u != nil {
			u.Accounting.ProviderRequestID = s.providerRequestID
			if u.Accounting.ServiceContext == "" {
				u.Accounting.ServiceContext = s.providerServiceTier
			}
			if s.ProviderEvidenceBuffer != nil {
				s.addAnthropicProviderEvidence(*u, v.Message.Usage)
			}
		}
	case anthropic.MessageDeltaEvent:
		if u := usageFromMessageDelta(v); u != nil {
			u.Accounting.ProviderRequestID = s.providerRequestID
			if u.Accounting.ServiceContext == "" {
				u.Accounting.ServiceContext = s.providerServiceTier
			}
			if err := s.pending.Push(*u); err != nil {
				return err
			}
			if s.cache != nil {
				s.captureEvidence(*u)
			}
			if s.ProviderEvidenceBuffer != nil {
				s.addAnthropicProviderEvidence(*u, v.Usage)
			}
		}
	case anthropic.ContentBlockStartEvent:
		cb := v.ContentBlock
		if media := assistantMediaEventsFromContentBlockStart(cb); len(media) > 0 {
			if err := s.ensureFrameStarted(); err != nil {
				return err
			}
			for _, e := range media {
				if err := s.pending.Push(e); err != nil {
					return err
				}
			}
		} else {
			switch cb.Type {
			case "thinking", "reasoning":
				thinking := cb.Thinking
				if thinking == "" && cb.Type == "reasoning" {
					var raw struct {
						Reasoning string `json:"reasoning"`
						Text      string `json:"text"`
					}
					if err := json.Unmarshal([]byte(cb.RawJSON()), &raw); err == nil {
						thinking = raw.Reasoning
						if thinking == "" {
							thinking = raw.Text
						}
					}
				}
				if thinking != "" || cb.Signature != "" {
					if err := s.ensureFrameStarted(); err != nil {
						return err
					}
				}
				if thinking != "" {
					if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: thinking}); err != nil {
						return err
					}
				}
				if cb.Signature != "" {
					if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventReasoningSignatureDelta, Signature: cb.Signature}); err != nil {
						return err
					}
				}
			case "tool_use":
				tu := cb.AsToolUse()
				s.activeToolID = tu.ID
				if err := s.pending.Push(lipapi.Event{
					Kind:       lipapi.EventToolCallStarted,
					ToolCallID: tu.ID,
					ToolName:   tu.Name,
				}); err != nil {
					return err
				}
			case "redacted_thinking":
				rt := cb.AsRedactedThinking()
				opaque, err := json.Marshal(map[string]string{
					"type": "redacted_thinking",
					"data": rt.Data,
				})
				if err != nil {
					return fmt.Errorf("anthropic: redacted_thinking opaque: %w", err)
				}
				if err := s.ensureFrameStarted(); err != nil {
					return err
				}
				if err := s.pending.Push(lipapi.Event{
					Kind:   lipapi.EventReasoningOpaqueDelta,
					Opaque: opaque,
				}); err != nil {
					return err
				}
			}
		}
	case anthropic.ContentBlockDeltaEvent:
		d := v.Delta
		if d.Type == "reasoning_delta" {
			thinking := d.Thinking
			if thinking == "" {
				var raw struct {
					Reasoning string `json:"reasoning"`
					Text      string `json:"text"`
				}
				if err := json.Unmarshal([]byte(d.RawJSON()), &raw); err == nil {
					thinking = raw.Reasoning
					if thinking == "" {
						thinking = raw.Text
					}
				}
			}
			if thinking != "" {
				if err := s.ensureFrameStarted(); err != nil {
					return err
				}
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: thinking}); err != nil {
					return err
				}
			}
			break
		}
		switch t := d.AsAny().(type) {
		case anthropic.TextDelta:
			if t.Text != "" {
				if err := s.ensureFrameStarted(); err != nil {
					return err
				}
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: t.Text}); err != nil {
					return err
				}
			}
		case anthropic.InputJSONDelta:
			if t.PartialJSON != "" {
				if err := s.pending.Push(lipapi.Event{
					Kind:       lipapi.EventToolCallArgsDelta,
					ToolCallID: s.activeToolID,
					Delta:      t.PartialJSON,
				}); err != nil {
					return err
				}
			}
		case anthropic.ThinkingDelta:
			if t.Thinking != "" {
				if err := s.ensureFrameStarted(); err != nil {
					return err
				}
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: t.Thinking}); err != nil {
					return err
				}
			}
		case anthropic.SignatureDelta:
			if t.Signature != "" {
				if err := s.ensureFrameStarted(); err != nil {
					return err
				}
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventReasoningSignatureDelta, Signature: t.Signature}); err != nil {
					return err
				}
			}
		}
	case anthropic.ContentBlockStopEvent:
		if s.activeToolID != "" {
			if err := s.pending.Push(lipapi.Event{
				Kind:       lipapi.EventToolCallFinished,
				ToolCallID: s.activeToolID,
			}); err != nil {
				return err
			}
			s.activeToolID = ""
		}
	case anthropic.MessageStopEvent:
		if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseFinished}); err != nil {
			return err
		}
		s.terminal = true
		s.completeCacheObservation()
	}
	return nil
}

// ensureFrameStarted emits ResponseStarted and MessageStarted if not already seen,
// so content-class deltas and assistant media refs are never published before the
// message frame. Mirrors the defensive establishment the TextDelta path already did
// inline; shared here so ThinkingDelta and SignatureDelta behave consistently.
func (s *msgStream) ensureFrameStarted() error {
	if !s.sawResp {
		s.sawResp = true
		if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted}); err != nil {
			return err
		}
	}
	if !s.sawMsg {
		s.sawMsg = true
		if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted}); err != nil {
			return err
		}
	}
	return nil
}

func usageFromMessageDelta(v anthropic.MessageDeltaEvent) *lipapi.Event {
	u := v.Usage
	in, inputPresent := anthropicUsageCount(u.InputTokens, u.JSON.InputTokens.Valid())
	out, outputPresent := anthropicUsageCount(u.OutputTokens, u.JSON.OutputTokens.Valid())
	cacheRead, cacheReadPresent := anthropicUsageCount(u.CacheReadInputTokens, u.JSON.CacheReadInputTokens.Valid())
	cacheWrite, cacheWritePresent := anthropicUsageCount(u.CacheCreationInputTokens, u.JSON.CacheCreationInputTokens.Valid())
	reasoning, reasoningPresent := anthropicUsageCount(u.OutputTokensDetails.ThinkingTokens, u.OutputTokensDetails.JSON.ThinkingTokens.Valid())
	presence := lipapi.UsagePresence{
		InputTokens: inputPresent, OutputTokens: outputPresent,
		CacheReadTokens: cacheReadPresent, CacheWriteTokens: cacheWritePresent,
		ReasoningTokens: reasoningPresent,
	}
	if !presence.Any() {
		return nil
	}
	total, totalPresent := anthropicSafeTotal(in, out)
	if inputPresent && outputPresent && totalPresent {
		presence.TotalTokens = true
	}
	ev := lipapi.Event{
		Kind:        lipapi.EventUsageDelta,
		InputTokens: in, OutputTokens: out, CacheReadTokens: cacheRead,
		CacheWriteTokens: cacheWrite, TotalTokens: total, ReasoningTokens: reasoning,
		UsagePresence: presence,
		RawUsageJSON:  rawUsageJSON(u.RawJSON(), u),
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:     lipapi.UsagePlaneProviderBillable,
			Source:    lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative,
			DedupeKey: "anthropic.messages:stream",
		},
	}
	return &ev
}

func usageFromMessageStart(v anthropic.MessageStartEvent) *lipapi.Event {
	u := v.Message.Usage
	in, inputPresent := anthropicUsageCount(u.InputTokens, u.JSON.InputTokens.Valid())
	out, outputPresent := anthropicUsageCount(u.OutputTokens, u.JSON.OutputTokens.Valid())
	cacheRead, cacheReadPresent := anthropicUsageCount(u.CacheReadInputTokens, u.JSON.CacheReadInputTokens.Valid())
	cacheWrite, cacheWritePresent := anthropicUsageCount(u.CacheCreationInputTokens, u.JSON.CacheCreationInputTokens.Valid())
	reasoning, reasoningPresent := anthropicUsageCount(u.OutputTokensDetails.ThinkingTokens, u.OutputTokensDetails.JSON.ThinkingTokens.Valid())
	presence := lipapi.UsagePresence{
		InputTokens: inputPresent, OutputTokens: outputPresent,
		CacheReadTokens: cacheReadPresent, CacheWriteTokens: cacheWritePresent,
		ReasoningTokens: reasoningPresent,
	}
	if !presence.Any() {
		return nil
	}
	total, totalPresent := anthropicSafeTotal(in, out)
	if inputPresent && outputPresent && totalPresent {
		presence.TotalTokens = true
	}
	return &lipapi.Event{
		Kind:        lipapi.EventUsageDelta,
		InputTokens: in, OutputTokens: out, CacheReadTokens: cacheRead,
		CacheWriteTokens: cacheWrite, ReasoningTokens: reasoning, TotalTokens: total,
		UsagePresence: presence,
		RawUsageJSON:  rawUsageJSON(u.RawJSON(), u),
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:             lipapi.UsagePlaneProviderBillable,
			Source:            lipapi.UsageSourceProviderReported,
			Authority:         lipapi.UsageAuthorityAuthoritative,
			DedupeKey:         "anthropic.messages:stream",
			ProviderRequestID: strings.TrimSpace(v.Message.ID),
			ServiceContext:    strings.TrimSpace(string(u.ServiceTier)),
		},
	}
}

// addAnthropicProviderEvidence keeps one cumulative provider snapshot for the
// stream. Anthropic message_start and message_delta usage objects are partial
// snapshots: a delta may contain only output while the start already carried
// input/cache fields. The canonical event remains the wire-shaped partial
// event, but the host-only V2 producer receives the merged snapshot so a
// revision never destructively drops fields from an earlier frame.
func (s *msgStream) addAnthropicProviderEvidence(ev lipapi.Event, usage any) {
	if s == nil || s.ProviderEvidenceBuffer == nil {
		return
	}
	if s.providerUsageSeen {
		s.providerUsage = mergeAnthropicUsageSnapshot(s.providerUsage, ev)
	} else {
		s.providerUsage = ev
		s.providerUsageSeen = true
	}
	s.providerUsageRaw = mergeAnthropicUsageRaw(s.providerUsageRaw, ev.RawUsageJSON)
	s.providerUsage.RawUsageJSON = s.providerUsageRaw
	s.Add(anthropicEvidenceDraftWithRaw(s.providerUsage, s.providerUsageRaw, "anthropic.messages.v2"))
}

func mergeAnthropicUsageSnapshot(previous, next lipapi.Event) lipapi.Event {
	out := previous
	out.Kind = next.Kind
	copyCounter := func(present bool, value int, dst *int, dstPresent *bool) {
		if !present {
			return
		}
		*dst = value
		*dstPresent = true
	}
	copyCounter(next.UsagePresence.InputTokens, next.InputTokens, &out.InputTokens, &out.UsagePresence.InputTokens)
	copyCounter(next.UsagePresence.OutputTokens, next.OutputTokens, &out.OutputTokens, &out.UsagePresence.OutputTokens)
	copyCounter(next.UsagePresence.CacheReadTokens, next.CacheReadTokens, &out.CacheReadTokens, &out.UsagePresence.CacheReadTokens)
	copyCounter(next.UsagePresence.CacheWriteTokens, next.CacheWriteTokens, &out.CacheWriteTokens, &out.UsagePresence.CacheWriteTokens)
	copyCounter(next.UsagePresence.ReasoningTokens, next.ReasoningTokens, &out.ReasoningTokens, &out.UsagePresence.ReasoningTokens)
	copyCounter(next.UsagePresence.TotalTokens, next.TotalTokens, &out.TotalTokens, &out.UsagePresence.TotalTokens)
	if strings.TrimSpace(next.Accounting.ProviderRequestID) != "" {
		out.Accounting.ProviderRequestID = strings.TrimSpace(next.Accounting.ProviderRequestID)
	}
	if strings.TrimSpace(next.Accounting.ProviderChargeID) != "" {
		out.Accounting.ProviderChargeID = strings.TrimSpace(next.Accounting.ProviderChargeID)
	}
	if strings.TrimSpace(next.Accounting.ProviderAccountKey) != "" {
		out.Accounting.ProviderAccountKey = strings.TrimSpace(next.Accounting.ProviderAccountKey)
	}
	if strings.TrimSpace(next.Accounting.ServiceContext) != "" {
		out.Accounting.ServiceContext = strings.TrimSpace(next.Accounting.ServiceContext)
	}
	if strings.TrimSpace(next.Accounting.DedupeKey) != "" {
		out.Accounting.DedupeKey = strings.TrimSpace(next.Accounting.DedupeKey)
	}
	return out
}

func mergeAnthropicUsageRaw(previous, next string) string {
	previous = strings.TrimSpace(previous)
	next = strings.TrimSpace(next)
	if previous == "" {
		return next
	}
	if next == "" {
		return previous
	}
	var dst, src map[string]json.RawMessage
	if json.Unmarshal([]byte(previous), &dst) != nil || json.Unmarshal([]byte(next), &src) != nil {
		return next
	}
	mergeAnthropicRawObject(dst, src)
	merged, err := json.Marshal(dst)
	if err != nil {
		return next
	}
	return string(merged)
}

func mergeAnthropicRawObject(dst, src map[string]json.RawMessage) {
	for key, value := range src {
		var dstNested, srcNested map[string]json.RawMessage
		if json.Unmarshal(dst[key], &dstNested) == nil && json.Unmarshal(value, &srcNested) == nil && dstNested != nil && srcNested != nil {
			mergeAnthropicRawObject(dstNested, srcNested)
			merged, err := json.Marshal(dstNested)
			if err == nil {
				dst[key] = merged
				continue
			}
		}
		dst[key] = value
	}
}

func anthropicUsageCount(value int64, present bool) (int, bool) {
	// Some Anthropic-compatible stream decoders expose a non-zero typed value
	// while omitting the SDK JSON presence marker. The value is still concrete
	// provider evidence; zero without presence remains absent.
	if (!present && value == 0) || value < 0 {
		return 0, false
	}
	converted := safecast.IntFromInt64Clamp(value)
	if int64(converted) != value {
		return 0, false
	}
	return converted, true
}

func anthropicSafeTotal(input, output int) (int, bool) {
	maxInt := int(^uint(0) >> 1)
	if input < 0 || output < 0 || input > maxInt-output {
		return 0, false
	}
	return input + output, true
}

func anthropicEvidenceDraft(ev lipapi.Event, usage any, mapping string) coremetering.ProviderEvidenceDraft {
	return anthropicEvidenceDraftWithRaw(ev, rawUsageJSONForAnthropic(usage), mapping)
}

func anthropicEvidenceDraftWithRaw(ev lipapi.Event, raw, mapping string) coremetering.ProviderEvidenceDraft {
	// Anthropic's documented usage object has no total_tokens field. The
	// canonical event keeps a convenience total for legacy consumers, but that
	// derived sum is not provider evidence and must not cross the V2 seam. If a
	// compatible provider really surfaces total_tokens, retain that native
	// value instead.
	providerEvent := ev
	if total, present := anthropicProviderTotalRaw(raw); present {
		providerEvent.TotalTokens = total
		providerEvent.UsagePresence.TotalTokens = true
	} else {
		providerEvent.TotalTokens = 0
		providerEvent.UsagePresence.TotalTokens = false
	}
	draft := coremetering.ProviderUsageEvent(providerEvent, mapping, "anthropic.messages:stream")
	draft.Measures = append(draft.Measures, anthropicLifetimeMeasuresRaw(raw)...)
	draft.Measures = append(draft.Measures, anthropicServerToolMeasuresRaw(raw)...)
	// Keep the original provider lexemes for fields whose V2 component is
	// represented by a qualifier or a count. The ordinary token evidence above
	// already owns the primary paths, so this helper only contributes additional
	// allowlisted locations and never creates duplicate evidence keys.
	draft.Evidence = append(draft.Evidence, anthropicSafeUsageEvidenceRaw(raw)...)
	if ev.Accounting.ServiceContext != "" {
		draft.Evidence = append(draft.Evidence, lipsdkSafeEvidence("$.provider_schema.service_tier", ev.Accounting.ServiceContext))
	}
	return draft
}

func anthropicProviderTotal(usage any) (int, bool) {
	return anthropicProviderTotalRaw(rawUsageJSONForAnthropic(usage))
}

func anthropicProviderTotalRaw(raw string) (int, bool) {
	if strings.TrimSpace(raw) == "" {
		return 0, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return 0, false
	}
	value, ok := fields["total_tokens"]
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(string(value)), 10, 64)
	if err != nil {
		return 0, false
	}
	return anthropicUsageCount(parsed, true)
}

func anthropicLifetimeMeasures(usage any) []lipsdkmetering.Measure {
	raw := rawUsageJSONForAnthropic(usage)
	if raw == "" {
		return nil
	}
	var u anthropic.Usage
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		return nil
	}
	return anthropicLifetimeMeasuresFromUsage(u)
}

func anthropicLifetimeMeasuresRaw(raw string) []lipsdkmetering.Measure {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var u anthropic.Usage
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		return nil
	}
	return anthropicLifetimeMeasuresFromUsage(u)
}

func anthropicLifetimeMeasuresFromUsage(u anthropic.Usage) []lipsdkmetering.Measure {
	measures := make([]lipsdkmetering.Measure, 0, 2)
	appendLifetime := func(name string, value int64, present bool) {
		if !present || value < 0 {
			return
		}
		decimal := lipsdkmetering.Decimal{Coefficient: strconv.FormatInt(value, 10)}
		measures = append(measures, lipsdkmetering.Measure{
			Key: lipsdkmetering.ComponentKey{
				Direction:  lipsdkmetering.DirectionInput,
				Component:  lipsdkmetering.ComponentCacheWriteInputToken,
				Unit:       lipsdkmetering.UnitToken,
				SchemaID:   lipsdkmetering.DefaultInclusionSchemaID,
				Dimensions: []lipsdkmetering.Dimension{{Name: "cache_lifetime", Value: name}},
			},
			Value: &decimal, Quality: lipsdkmetering.QualityObserved,
			MethodRef: "anthropic.messages.cache_lifetime.v1",
		})
	}
	appendLifetime("5m", u.CacheCreation.Ephemeral5mInputTokens, u.CacheCreation.JSON.Ephemeral5mInputTokens.Valid())
	appendLifetime("1h", u.CacheCreation.Ephemeral1hInputTokens, u.CacheCreation.JSON.Ephemeral1hInputTokens.Valid())
	return measures
}

func anthropicServerToolMeasures(usage any) []lipsdkmetering.Measure {
	return anthropicServerToolMeasuresRaw(rawUsageJSONForAnthropic(usage))
}

func anthropicServerToolMeasuresRaw(raw string) []lipsdkmetering.Measure {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var usage anthropic.Usage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		return nil
	}
	return anthropicServerToolMeasuresFromUsage(usage.ServerToolUse)
}

func anthropicServerToolMeasuresFromUsage(server anthropic.ServerToolUsage) []lipsdkmetering.Measure {
	measures := make([]lipsdkmetering.Measure, 0, 2)
	appendTool := func(kind string, value int64, present bool) {
		if !present || value < 0 {
			return
		}
		decimal := lipsdkmetering.Decimal{Coefficient: strconv.FormatInt(value, 10)}
		measures = append(measures, lipsdkmetering.Measure{
			Key: lipsdkmetering.ComponentKey{
				Direction:  lipsdkmetering.DirectionNone,
				Component:  lipsdkmetering.ComponentToolQuery,
				Unit:       lipsdkmetering.UnitCount,
				SchemaID:   lipsdkmetering.DefaultInclusionSchemaID,
				Dimensions: []lipsdkmetering.Dimension{{Name: "tool", Value: kind}},
			},
			Value: &decimal, Quality: lipsdkmetering.QualityObserved,
			MethodRef: "anthropic.messages.server_tool.v1",
		})
	}
	appendTool("web_fetch", server.WebFetchRequests, server.JSON.WebFetchRequests.Valid())
	appendTool("web_search", server.WebSearchRequests, server.JSON.WebSearchRequests.Valid())
	return measures
}

func anthropicSafeUsageEvidence(usage any) []lipsdkmetering.SafeEvidenceField {
	return anthropicSafeUsageEvidenceRaw(rawUsageJSONForAnthropic(usage))
}

func anthropicSafeUsageEvidenceRaw(raw string) []lipsdkmetering.SafeEvidenceField {
	if raw == "" {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil
	}
	result := make([]lipsdkmetering.SafeEvidenceField, 0, 4)
	appendRawCount := func(fields map[string]json.RawMessage, name, path string) {
		value, ok := fields[name]
		if !ok || strings.TrimSpace(string(value)) == "null" {
			return
		}
		lexeme := strings.TrimSpace(string(value))
		decimal, err := lipsdkmetering.ParseDecimal(lexeme)
		if err != nil || decimal.Scale != 0 || strings.HasPrefix(decimal.Coefficient, "-") {
			return
		}
		if _, err := strconv.ParseInt(decimal.Coefficient, 10, 64); err != nil {
			return
		}
		result = append(result, lipsdkSafeEvidence(path, lexeme))
	}
	var cacheCreation map[string]json.RawMessage
	if nested, ok := fields["cache_creation"]; ok && json.Unmarshal(nested, &cacheCreation) == nil {
		appendRawCount(cacheCreation, "ephemeral_5m_input_tokens", "$.usage.cache_creation_5m_input_tokens")
		appendRawCount(cacheCreation, "ephemeral_1h_input_tokens", "$.usage.cache_creation_1h_input_tokens")
	}
	var serverToolUse map[string]json.RawMessage
	if nested, ok := fields["server_tool_use"]; ok && json.Unmarshal(nested, &serverToolUse) == nil {
		appendRawCount(serverToolUse, "web_fetch_requests", "$.usage.web_fetch_requests")
		appendRawCount(serverToolUse, "web_search_requests", "$.usage.web_search_requests")
	}
	return result
}

func rawUsageJSONForAnthropic(usage any) string {
	switch u := usage.(type) {
	case anthropic.Usage:
		return rawUsageJSON(u.RawJSON(), u)
	case anthropic.MessageDeltaUsage:
		return rawUsageJSON(u.RawJSON(), u)
	default:
		return ""
	}
}

func lipsdkSafeEvidence(path, lexeme string) lipsdkmetering.SafeEvidenceField {
	return lipsdkmetering.SafeEvidenceField{Path: path, Lexeme: lexeme, Present: true, Acquisition: lipsdkmetering.AcquisitionProviderResponse}
}

func rawUsageJSON(raw string, usage any) string {
	if raw != "" {
		return raw
	}
	b, err := json.Marshal(usage)
	if err != nil {
		return ""
	}
	return string(b)
}

func (s *msgStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	var err error
	s.closeOnce.Do(func() {
		if s.sdk != nil {
			err = s.sdk.Close()
		}
	})
	return err
}

func (s *msgStream) Cancel(_ context.Context, _ leglifecycle.CancelCause) leglifecycle.CancelResult {
	err := s.Close()
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeTransport, Err: err}
}
