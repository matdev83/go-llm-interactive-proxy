package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/codex/internal/routingstub"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	nativeContextLiveGate      = "LIP_CODEX_NATIVE_CONTEXT_LIVE"
	nativeContextLiveModelEnv  = "LIP_CODEX_NATIVE_CONTEXT_MODEL"
	nativeContextLiveTokenEnv  = "LIP_CODEX_NATIVE_CONTEXT_TOKEN"
	nativeContextLiveAuthEnv   = "LIP_CODEX_NATIVE_CONTEXT_AUTH_JSON"
	nativeContextLiveBaseEnv   = "LIP_CODEX_NATIVE_CONTEXT_BASE_URL"
	nativeContextLiveModel     = "gpt-5.3-codex-spark"
	nativeContextLiveTail      = "LIVE_NATIVE_CONTEXT_TAIL_7B2A: answer with one short word."
	nativeContextLiveTailTwo   = "LIVE_NATIVE_CONTEXT_TAIL_SECOND_91C4: answer with one short word."
	nativeContextLiveTailThree = "LIVE_NATIVE_CONTEXT_TAIL_THIRD_5E77: answer with one short word."
	nativeContextLiveTailFour  = "LIVE_NATIVE_CONTEXT_TAIL_FOURTH_2D18: answer with one short word."
)

// TestNativeContextCodexLive is the original opt-in compatibility probe. Keep
// it separate from the strict automatic-compaction proof below.
func TestNativeContextCodexLive(t *testing.T) {
	if os.Getenv(nativeContextLiveGate) != "1" {
		t.Skip("set LIP_CODEX_NATIVE_CONTEXT_LIVE=1 to opt into live Codex validation")
	}
	baseURL := strings.TrimSpace(os.Getenv(nativeContextLiveBaseEnv))
	token := strings.TrimSpace(os.Getenv(nativeContextLiveTokenEnv))
	model := strings.TrimSpace(os.Getenv(nativeContextLiveModelEnv))
	if baseURL == "" || token == "" || model == "" {
		t.Skip("compatibility probe requires explicit base URL, token, and model; use TestNativeContextAutomaticCompactionLive for auth.json resolution")
	}
	if !strings.Contains(strings.ToLower(model), "codex") {
		t.Skip("live validation requires a Codex model identifier")
	}
	engine, err := New(Config{
		BaseURL: baseURL, AccessToken: token, Models: []string{model}, Transport: TransportHTTPS,
		NativeContext: &NativeContextConfig{
			Enabled: true, RequestEncryptedReasoning: true, ReasoningContinuity: ContinuityBestEffort,
			Compaction: NativeCompactionConfig{Enabled: true, TriggerTokens: 1000, RetainedMessageTokens: 32, MinSavingsTokens: 1, StateTTL: time.Hour, MaxEntries: 4, MaxEntryBytes: 1 << 20, FailureCooldown: time.Minute},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	marker := func() map[string]json.RawMessage {
		return map[string]json.RawMessage{nativeContinuityMarkerKey: json.RawMessage(nativeContinuityMarkerValue)}
	}
	first := lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "live-validation"}, Extensions: marker(), Messages: []lipapi.Message{nativeText(lipapi.RoleUser, "State one short deterministic fact for the next turn.")}}
	firstEvents := collectNativeEvents(t, engine, first, model)
	assertLiveReasoning(t, firstEvents, "normal encrypted reasoning")
	var exact *lipapi.ReasoningPart
	for _, ev := range firstEvents {
		if ev.Kind == lipapi.EventReasoningPart && ev.Reasoning != nil {
			exact = ev.Reasoning
			break
		}
	}
	if exact == nil {
		t.Fatal("live response contained no exact reasoning part")
	}
	second := lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "live-validation"}, Extensions: marker(), Messages: []lipapi.Message{{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{Kind: lipapi.PartReasoning, Reasoning: exact}}}, nativeText(lipapi.RoleUser, strings.Repeat("Keep the prior fact available. ", 80))}}
	secondEvents := collectNativeEvents(t, engine, second, model)
	assertLiveReasoning(t, secondEvents, "post-replay reasoning")
	third := lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "live-validation"}, Extensions: marker(), Messages: append(second.Messages, nativeText(lipapi.RoleUser, "Use the retained fact once."))}
	thirdEvents := collectNativeEvents(t, engine, third, model)
	assertLiveReasoning(t, thirdEvents, "post-compaction reasoning")
}

func assertLiveReasoning(t *testing.T, events []lipapi.Event, label string) {
	t.Helper()
	for _, event := range events {
		if event.Kind == lipapi.EventReasoningPart && event.Reasoning != nil && len(event.Reasoning.Opaque) > 0 {
			return
		}
	}
	t.Fatalf("%s did not produce a bounded exact reasoning part", label)
}

// TestNativeContextAutomaticCompactionLive is the opt-in, billable proof that
// the public Engine.Open path performs native compaction against Codex. It does
// not call NativeContextCoordinator or Compact directly. The transport observer
// records only bounded request-shape booleans and status/model metadata.
func TestNativeContextAutomaticCompactionLive(t *testing.T) {
	if os.Getenv(nativeContextLiveGate) != "1" {
		t.Skip("set LIP_CODEX_NATIVE_CONTEXT_LIVE=1 to opt into the billable live Codex proof")
	}

	model := strings.TrimSpace(os.Getenv(nativeContextLiveModelEnv))
	if model == "" {
		model = nativeContextLiveModel
	}
	if !strings.Contains(strings.ToLower(model), "codex") {
		t.Fatalf("live Codex setup failed: unsupported model configuration (set %s to a Codex model)", nativeContextLiveModelEnv)
	}

	token := strings.TrimSpace(os.Getenv(nativeContextLiveTokenEnv))
	authPath := strings.TrimSpace(os.Getenv(nativeContextLiveAuthEnv))
	if authPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("live Codex auth unavailable: cannot resolve the user home directory; use %s", nativeContextLiveAuthEnv)
		}
		authPath = filepath.Join(home, ".codex", "auth.json")
	}
	if token == "" {
		if _, err := os.Stat(authPath); err != nil {
			t.Fatalf("live Codex auth unavailable: provide %s or ensure the Codex auth.json path exists", nativeContextLiveTokenEnv)
		}
	}

	baseURL := strings.TrimSpace(os.Getenv(nativeContextLiveBaseEnv))
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	observer := newNativeLiveHTTPRecorder("", http.DefaultTransport)
	observer.SetExpectedPhaseTails(
		[]string{"", nativeContextLiveTailTwo, nativeContextLiveTailThree, nativeContextLiveTailFour},
		[]string{nativeContextLiveTailTwo, nativeContextLiveTailThree, nativeContextLiveTailFour},
	)
	client := &http.Client{Transport: observer, Timeout: 2 * time.Minute}
	engine, err := New(Config{
		BaseURL:      baseURL,
		AccessToken:  token,
		AuthJSONPath: authPath,
		HTTPClient:   client,
		Models:       []string{model},
		Transport:    TransportHTTPS,
		// Keep the first probe's normal request minimal: the ordinary Codex
		// connector's early-session text-verbosity compatibility bump is not part
		// of the encrypted-reasoning compatibility question.
		EarlySessionVerbosityBumpDisabled: true,
		MidSessionVerbosityBumpDisabled:   true,
		NativeContext: &NativeContextConfig{
			Enabled:                   true,
			RequestEncryptedReasoning: true,
			ReasoningContinuity:       ContinuityRequired,
			Compaction: NativeCompactionConfig{
				// Cross 256 with the synthetic transcript, then keep the compact
				// replacement plus the next tiny append below the same trigger.
				Enabled:               true,
				TriggerTokens:         256,
				RetainedMessageTokens: 32,
				MinSavingsTokens:      1,
				StateTTL:              time.Hour,
				MaxEntries:            4,
				MaxEntryBytes:         1 << 20,
				FailureCooldown:       time.Minute,
			},
		},
	})
	if err != nil {
		t.Fatalf("live Codex setup failed: %s", classifyNativeContextLiveError(err, nil))
	}
	t.Cleanup(func() { _ = engine.Close() })

	firstCall := nativeContextLiveCall([]lipapi.Message{
		nativeText(lipapi.RoleUser, "State one short deterministic fact for the next turn."),
	})
	firstEvents, err := collectNativeContextLiveEvents(engine, firstCall, model)
	if err != nil {
		t.Fatalf("live Codex first request failed: %s", classifyNativeContextLiveError(err, observer))
	}
	exact := firstLiveReasoning(firstEvents)
	if exact == nil {
		t.Fatal("live Codex first request produced no bounded completed encrypted reasoning item")
	}
	firstProof := observer.CodexSnapshot()
	if len(firstProof) != 1 || firstProof[0].Compaction || !firstProof[0].HasReasoning || !firstProof[0].IncludeEncrypted || firstProof[0].ReasoningEffortPresent || firstProof[0].ToolCount != 0 || firstProof[0].InputTypeCounts["message"] != 1 {
		t.Fatalf("live Codex first normal request was not minimal encrypted-reasoning shape: %#v", firstProof)
	}
	assistantText := firstLiveAssistantText(firstEvents)
	if assistantText == "" {
		t.Fatal("live Codex first request produced no assistant text for the replay transcript")
	}
	assertNoClientVisibleCompaction(t, firstEvents, "first request")

	secondMessages := nativeContextLiveHistory(firstCall.Messages[0], assistantText, exact, nativeContextLiveTailTwo+" "+strings.Repeat("safe prior context ", 100))
	secondCall := nativeContextLiveCall(secondMessages)
	secondEvents, err := collectNativeContextLiveEvents(engine, secondCall, model)
	if err != nil {
		t.Fatalf("live Codex automatic-compaction request failed: %s", classifyNativeContextLiveError(err, observer))
	}
	assertNoClientVisibleCompaction(t, secondEvents, "request after compaction")

	thirdMessages := append(append([]lipapi.Message(nil), secondMessages...), nativeText(lipapi.RoleUser, nativeContextLiveTailThree+" Follow up now."))
	thirdCall := nativeContextLiveCall(thirdMessages)
	thirdEvents, err := collectNativeContextLiveEvents(engine, thirdCall, model)
	if err != nil {
		t.Fatalf("live Codex checkpoint-reuse request failed: %s", classifyNativeContextLiveError(err, observer))
	}
	assertNoClientVisibleCompaction(t, thirdEvents, "checkpoint reuse")
	fourthMessages := append(append([]lipapi.Message(nil), thirdMessages...), nativeText(lipapi.RoleUser, nativeContextLiveTailFour))
	fourthEvents, err := collectNativeContextLiveEvents(engine, nativeContextLiveCall(fourthMessages), model)
	if err != nil {
		t.Fatalf("live Codex post-second-compaction request failed: %s", classifyNativeContextLiveError(err, observer))
	}
	assertNoClientVisibleCompaction(t, fourthEvents, "post-second-compaction normal request")

	proof := observer.CodexSnapshot()
	t.Logf("native compaction request sequence: %s", nativeLiveRequestSequence(proof))
	if len(proof) < 3 {
		t.Fatalf("live Codex proof observed %d upstream requests; want at least one normal, one V2 compaction, and one normal request", len(proof))
	}
	for i, item := range proof {
		if item.StatusCode < 200 || item.StatusCode >= 300 {
			t.Fatalf("live Codex proof request %d was not successful: %#v", i+1, item)
		}
	}
	if proof[0].Compaction || proof[0].TriggerCount != 0 || proof[0].StatusCode < 200 || proof[0].StatusCode >= 300 {
		t.Fatalf("live Codex proof first request shape was not a successful normal request: %#v", proof[0])
	}
	if !proof[1].Compaction || !strings.HasSuffix(proof[1].Path, "/responses/compact") || proof[1].TriggerCount != 0 || proof[1].TriggerSemantics != "dedicated_endpoint" || proof[1].StatusCode < 200 || proof[1].StatusCode >= 300 {
		t.Fatalf("live Codex proof did not observe a successful dedicated compaction endpoint call: %#v", proof[1])
	}
	compactionRequests := 0
	for i, item := range proof {
		if !item.Compaction {
			continue
		}
		compactionRequests++
		if item.ResponseObject != "response.compaction" || item.ResponseOutputTypes["compaction_summary"] != 1 || item.ResponseOutputTypes["message"] == 0 {
			t.Fatalf("live Codex compact response shape was not authoritative: request=%d evidence=%#v", i+1, item)
		}
		if !item.CompactionTailExcluded {
			t.Fatalf("live Codex compaction request included the latest user tail: request=%d evidence=%#v", i+1, item)
		}
		if i+1 >= len(proof) || proof[i+1].Compaction || !proof[i+1].HasCheckpoint || !proof[i+1].TailPreserved || proof[i+1].TriggerCount != 0 {
			t.Fatalf("live Codex normal after compaction did not retain summary and tail: compaction_request=%d evidence=%#v", i+1, item)
		}
	}
	if compactionRequests < 1 {
		t.Fatal("live Codex proof did not observe an automatic dedicated compaction")
	}
	for i, item := range proof {
		if item.Compaction || i == 0 {
			continue
		}
		if !item.HasCheckpoint || !item.HasSummary || !item.TailPreserved || item.TriggerCount != 0 {
			t.Fatalf("live Codex normal request after first compaction lost checkpoint or tail: request=%d evidence=%#v sequence=%s", i+1, item, nativeLiveRequestSequence(proof))
		}
	}
	for i, item := range proof {
		if item.MarkerLeaked {
			t.Fatalf("live Codex request %d forwarded the internal continuity marker", i+1)
		}
		if item.Model != model && normalizeCodexModel(item.Model) != normalizeCodexModel(model) {
			t.Fatalf("live Codex request %d used an unexpected model shape", i+1)
		}
	}

	if engine.rt == nil || engine.rt.native == nil {
		t.Fatal("live Codex proof could not inspect connector-private native telemetry")
	}
	telemetry := engine.rt.native.telemetry.snapshot()
	if telemetry.CompactionAttempts+telemetry.CompactionSecondAttempts != int64(compactionRequests) || telemetry.CompactionSuccesses != int64(compactionRequests) {
		t.Fatalf("live Codex telemetry reported attempts=%d second_attempts=%d successes=%d; want one successful outcome per automatic compaction", telemetry.CompactionAttempts, telemetry.CompactionSecondAttempts, telemetry.CompactionSuccesses)
	}
	if telemetry.CompactionProtocolFails != 0 || telemetry.CompactionRewriteFails != 0 || telemetry.CheckpointCommits != int64(compactionRequests) {
		t.Fatalf("live Codex preparation outcome was not clean for every compaction: %s", telemetry)
	}
	if compactionRequests == 1 && telemetry.CheckpointReuseHits < 1 {
		t.Fatalf("live Codex telemetry reported no checkpoint reuse after a single compaction: %s", telemetry)
	}
	if telemetry.ReasoningRequested < 3 {
		t.Fatalf("live Codex telemetry reported reasoning requests=%d; want encrypted reasoning on all eligible normal turns", telemetry.ReasoningRequested)
	}
	proofCompactionRequests := compactionRequests
	compactionRequests, triggers := 0, 0
	triggerSemantics := ""
	for _, item := range proof {
		if item.Compaction {
			compactionRequests++
			triggers += item.TriggerCount
			triggerSemantics = item.TriggerSemantics
		}
	}
	if compactionRequests < 1 || triggers != 0 || triggerSemantics != "dedicated_endpoint" {
		t.Fatalf("live Codex proof observed compaction_requests=%d triggers=%d semantics=%q; want successful dedicated endpoint calls", compactionRequests, triggers, triggerSemantics)
	}

	accountID := strings.TrimSpace(engine.rt.cfg.AccountID)
	if accountID == "" {
		accountID = "static"
	}
	storeShape := nativeLiveCheckpointStoreShape(engine.rt.native.store, nativeLiveCheckpointExpectation{
		SessionID: firstCall.Session.AuthoritativeSessionID,
		AccountID: accountID,
		Model:     model,
	})
	if !storeShape.MatchingCommitted {
		t.Fatalf("live Codex checkpoint store shape did not show a committed authoritative checkpoint: %+v", storeShape)
	}

	clientVisibleCompaction := containsClientVisibleCompaction(firstEvents) ||
		containsClientVisibleCompaction(secondEvents) || containsClientVisibleCompaction(thirdEvents)
	if clientVisibleCompaction {
		t.Fatal("live Codex proof exposed an internal compaction item to the client stream")
	}
	usageEvidence, err := classifyCompactionUsageEvidence(secondEvents)
	if err != nil {
		t.Fatalf("live Codex surfaced malformed optional usage evidence: %v", err)
	}
	if usageEvidence == "absent" {
		t.Fatal("live Codex compaction did not surface charged provider or estimated usage evidence")
	}
	normalEvents := append(append([]lipapi.Event(nil), secondEvents...), thirdEvents...)
	secondCompaction := proofCompactionRequests > 1
	lastNormal := proof[len(proof)-1]
	providerUsagePresent := false
	for _, item := range proof {
		if item.Compaction && item.ResponseUsagePresent {
			providerUsagePresent = true
		}
	}
	t.Logf("native compaction proof PASS: request_count=%d compaction_requests=%d triggers=%d trigger_semantics=%s checkpoint_committed=%t checkpoint_reused=%t second_compaction=%t latest_tail_preserved=%t client_visible_compaction=%t provider_usage_present=%t usage_evidence=%s normal_unscoped_usage_events=%d",
		len(proof), compactionRequests, triggers, triggerSemantics, storeShape.MatchingCommitted, telemetry.CheckpointReuseHits > 0,
		secondCompaction, lastNormal.TailPreserved, clientVisibleCompaction, providerUsagePresent,
		usageEvidence, countUnscopedUsageEvents(normalEvents))
}

func nativeContextLiveCall(messages []lipapi.Message) lipapi.Call {
	return lipapi.Call{
		Session: lipapi.SessionRef{AuthoritativeSessionID: "live-native-context-proof"},
		// This value represents the proxy/feature-injected marker at the
		// backend-plugin boundary; it is not a client session hint.
		Extensions: map[string]json.RawMessage{
			nativeContinuityMarkerKey: json.RawMessage(nativeContinuityMarkerValue),
		},
		Messages: messages,
	}
}

func nativeContextLiveHistory(firstUser lipapi.Message, assistantText string, reasoning *lipapi.ReasoningPart, tail string) []lipapi.Message {
	history := []lipapi.Message{
		firstUser,
		{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{
			lipapi.TextPart(assistantText),
			{Kind: lipapi.PartReasoning, Reasoning: reasoning},
		}},
		nativeText(lipapi.RoleUser, strings.Repeat("safe prior context ", 72)),
		nativeText(lipapi.RoleAssistant, "The safe prior context remains available."),
		nativeText(lipapi.RoleUser, tail),
	}
	return history
}

func firstLiveAssistantText(events []lipapi.Event) string {
	var b strings.Builder
	for _, event := range events {
		if event.Kind == lipapi.EventTextDelta {
			b.WriteString(event.Delta)
		}
	}
	return strings.TrimSpace(b.String())
}

func collectNativeContextLiveEvents(engine *Engine, call lipapi.Call, model string) ([]lipapi.Event, error) {
	stream, err := engine.Open(context.Background(), &call, routingstub.AttemptCandidate{Primary: routingstub.Primary{Model: model}})
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	var events []lipapi.Event
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			return events, nil
		}
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
}

func firstLiveReasoning(events []lipapi.Event) *lipapi.ReasoningPart {
	for _, event := range events {
		if event.Kind == lipapi.EventReasoningPart && event.Reasoning != nil && len(event.Reasoning.Opaque) > 0 {
			copied := *event.Reasoning
			copied.Opaque = append([]byte(nil), event.Reasoning.Opaque...)
			return &copied
		}
	}
	return nil
}

func assertNoClientVisibleCompaction(t *testing.T, events []lipapi.Event, label string) {
	t.Helper()
	for _, event := range events {
		if event.Kind == lipapi.EventItem && event.Item != nil && event.Item.Kind == lipapi.ItemKindCompaction {
			t.Fatalf("%s exposed an internal compaction item to the client stream", label)
		}
	}
}

// validateCompactionUsageEvidence deliberately identifies the connector-private
// scoped event instead of relying on a transport/frontend-specific event index.
// Usage is allowed around the message-start repair, but must precede the first
// client-visible content event.
func validateCompactionUsageEvidence(events []lipapi.Event) error {
	type candidate struct {
		index int
		event lipapi.Event
		scope lipapi.ScopedUsageDelta
	}
	var candidates []candidate
	firstContent := -1
	for index, event := range events {
		if firstContent < 0 && isNativeContentEvent(event.Kind) {
			firstContent = index
		}
		if event.Kind != lipapi.EventUsageDelta || len(event.UsageScopes) != 1 {
			continue
		}
		// Native compaction evidence is a separate scoped event and must not
		// masquerade as the normal client-visible legacy usage counters.
		if event.InputTokens != 0 || event.OutputTokens != 0 || event.TotalTokens != 0 {
			continue
		}
		candidates = append(candidates, candidate{index: index, event: event, scope: event.UsageScopes[0]})
	}
	if len(candidates) != 1 {
		return fmt.Errorf("automatic compaction usage proof failed: want exactly one separate scoped usage event, got %d; event_shape=%s", len(candidates), nativeLiveEventShape(events))
	}
	selected := candidates[0]
	if selected.event.Accounting.Plane != lipapi.UsagePlaneProviderBillable || selected.scope.Accounting.Plane != lipapi.UsagePlaneProviderBillable {
		return fmt.Errorf("automatic compaction usage proof failed: scoped evidence was not provider-billable; event_shape=%s", nativeLiveEventShape(events))
	}
	providerAuthoritative := selected.event.Accounting.Source == lipapi.UsageSourceProviderReported && selected.scope.Accounting.Source == lipapi.UsageSourceProviderReported && selected.event.Accounting.Authority == lipapi.UsageAuthorityAuthoritative && selected.scope.Accounting.Authority == lipapi.UsageAuthorityAuthoritative
	estimatedLocal := selected.event.Accounting.Source == lipapi.UsageSourceLocalEstimator && selected.scope.Accounting.Source == lipapi.UsageSourceLocalEstimator && selected.event.Accounting.Authority == lipapi.UsageAuthorityEstimated && selected.scope.Accounting.Authority == lipapi.UsageAuthorityEstimated
	if !providerAuthoritative && !estimatedLocal {
		return fmt.Errorf("automatic compaction usage proof failed: scoped evidence had unsupported source/authority; event_shape=%s", nativeLiveEventShape(events))
	}
	if !selected.scope.UsagePresence.Any() ||
		(selected.scope.UsagePresence.InputTokens && selected.scope.InputTokens <= 0) ||
		(selected.scope.UsagePresence.OutputTokens && selected.scope.OutputTokens <= 0) ||
		(selected.scope.UsagePresence.TotalTokens && selected.scope.TotalTokens <= 0) ||
		selected.scope.InputTokens <= 0 && selected.scope.OutputTokens <= 0 && selected.scope.TotalTokens <= 0 {
		return fmt.Errorf("automatic compaction usage proof failed: provider usage had no positive present counter; event_shape=%s", nativeLiveEventShape(events))
	}
	if firstContent >= 0 && selected.index >= firstContent {
		return fmt.Errorf("automatic compaction usage proof failed: scoped provider usage occurred after client-visible content; event_shape=%s", nativeLiveEventShape(events))
	}
	return nil
}

func classifyCompactionUsageEvidence(events []lipapi.Event) (string, error) {
	for _, event := range events {
		if event.Kind == lipapi.EventUsageDelta && len(event.UsageScopes) > 0 {
			if err := validateCompactionUsageEvidence(events); err != nil {
				return "", err
			}
			source, authority, count := compactionUsageSummary(events)
			if count != 1 {
				return "", fmt.Errorf("automatic compaction usage proof found %d scoped events", count)
			}
			if source == lipapi.UsageSourceProviderReported && authority == lipapi.UsageAuthorityAuthoritative {
				return "provider", nil
			}
			if source == lipapi.UsageSourceLocalEstimator && authority == lipapi.UsageAuthorityEstimated {
				return "estimated", nil
			}
			return "", fmt.Errorf("automatic compaction usage proof found unsupported source/authority")
		}
	}
	return "absent", nil
}

func compactionUsageSummary(events []lipapi.Event) (lipapi.UsageSource, lipapi.UsageAuthority, int) {
	var source lipapi.UsageSource
	var authority lipapi.UsageAuthority
	count := 0
	for _, event := range events {
		if event.Kind != lipapi.EventUsageDelta || len(event.UsageScopes) != 1 || event.InputTokens != 0 || event.OutputTokens != 0 || event.TotalTokens != 0 {
			continue
		}
		count++
		source = event.UsageScopes[0].Accounting.Source
		authority = event.UsageScopes[0].Accounting.Authority
	}
	return source, authority, count
}

func countUnscopedUsageEvents(events []lipapi.Event) int {
	count := 0
	for _, event := range events {
		if event.Kind == lipapi.EventUsageDelta && len(event.UsageScopes) == 0 {
			count++
		}
	}
	return count
}

func containsClientVisibleCompaction(events []lipapi.Event) bool {
	for _, event := range events {
		if event.Kind == lipapi.EventItem && event.Item != nil && event.Item.Kind == lipapi.ItemKindCompaction {
			return true
		}
	}
	return false
}

type nativeLiveCheckpointExpectation struct {
	SessionID string
	AccountID string
	Model     string
}

// nativeLiveCheckpointStoreShape is deliberately test-only and reports only
// bounded counts/booleans. It never exposes checkpoint contents or fingerprints.
type nativeLiveCheckpointStoreObservation struct {
	EntryCount           int
	MatchingEntries      int
	AuthoritativeSession bool
	AccountMatches       bool
	ModelMatches         bool
	ReplacementPresent   bool
	MatchingCommitted    bool
}

func nativeLiveCheckpointStoreShape(store *nativeCheckpointStore, expected nativeLiveCheckpointExpectation) nativeLiveCheckpointStoreObservation {
	shape := nativeLiveCheckpointStoreObservation{}
	if store == nil {
		return shape
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, entry := range store.entries {
		shape.EntryCount++
		key := entry.checkpoint.Key
		authoritativeSession := key.SessionID == expected.SessionID
		accountMatches := key.AccountID == expected.AccountID && key.AccountID != ""
		modelMatches := normalizeCodexModel(key.Model) == normalizeCodexModel(expected.Model) && key.Model != ""
		replacementPresent := len(entry.checkpoint.Replacement) > 0
		if authoritativeSession {
			shape.AuthoritativeSession = true
		}
		if accountMatches {
			shape.AccountMatches = true
		}
		if modelMatches {
			shape.ModelMatches = true
		}
		if replacementPresent {
			shape.ReplacementPresent = true
		}
		if authoritativeSession && accountMatches && modelMatches {
			shape.MatchingEntries++
			shape.MatchingCommitted = replacementPresent
		}
	}
	return shape
}

// nativeLiveEventShape reports event structure only. It intentionally excludes
// deltas, item payloads, token values, and other provider/client content.
func nativeLiveEventShape(events []lipapi.Event) string {
	parts := make([]string, 0, len(events))
	for index, event := range events {
		part := fmt.Sprintf("%d:%s", index, event.Kind)
		if event.Kind == lipapi.EventUsageDelta {
			part += fmt.Sprintf("[scopes=%d plane=%s source=%s authority=%s presence=%+v]", len(event.UsageScopes), event.Accounting.Plane, event.Accounting.Source, event.Accounting.Authority, event.UsagePresence)
			for _, scope := range event.UsageScopes {
				part += fmt.Sprintf("[scope plane=%s source=%s authority=%s presence=%+v]", scope.Accounting.Plane, scope.Accounting.Source, scope.Accounting.Authority, scope.UsagePresence)
			}
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ",")
}

func classifyNativeContextLiveError(err error, recorder *nativeLiveHTTPRecorder) string {
	if err == nil {
		return "unknown"
	}
	status := nativeContextStatus(err)
	if recorder != nil {
		records := recorder.Snapshot()
		for _, evidence := range records {
			if evidence.StatusCode >= 400 {
				status = evidence.StatusCode
				break
			}
		}
		if len(records) > 0 {
			last := records[len(records)-1]
			return fmt.Sprintf("err=%v status_source=%d sequence=%s last={path=%s phase=%s status=%d category=%s code=%s type=%s param=%s shape={model=%s fields=%s input_types=%v reasoning=%t effort_present=%t include_encrypted=%t tools=%d previous_id=%t metadata_keys=%v response_object=%s response_status=%s response_fields=%s response_output_shape=%s response_output_types=%v response_usage_present=%t response_compact_field=%t}}",
				err, status, nativeLiveRequestSequence(records),
				last.Path, last.Phase, last.StatusCode, last.Category, last.ProviderCode, last.ProviderType, last.ProviderParam,
				last.Model, strings.Join(last.TopLevelFields, ","), last.InputTypeCounts, last.HasReasoning,
				last.ReasoningEffortPresent, last.IncludeEncrypted, last.ToolCount, last.PreviousResponseID, last.MetadataKeys,
				last.ResponseObject, last.ResponseStatus, strings.Join(last.ResponseTopLevelFields, ","), last.ResponseOutputShape, last.ResponseOutputTypes, last.ResponseUsagePresent, last.ResponseCompactField)
		}
	}
	if status != 0 {
		return fmt.Sprintf("phase=connector status=%d category=%s", status, nativeLiveStatusCategory(status))
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "phase=connector status=0 category=timeout"
	}
	return "phase=connector status=0 category=provider_or_connector_failure"
}

// nativeLiveRequestSequence reports only bounded request-shape evidence. It is
// intended for failures, so a second compaction or a checkpoint miss remains
// diagnosable without retaining prompt, response, or opaque reasoning content.
func nativeLiveRequestSequence(records []nativeLiveRequestEvidence) string {
	parts := make([]string, 0, len(records))
	for i, record := range records {
		parts = append(parts, fmt.Sprintf("%d:{path=%s phase=%s status=%d checkpoint=%t summary=%t input_types=%v tail=%t compact_tail_excluded=%t}",
			i+1, record.Path, record.Phase, record.StatusCode, record.HasCheckpoint, record.HasSummary,
			record.InputTypeCounts, record.TailPreserved, record.CompactionTailExcluded))
	}
	return strings.Join(parts, ";")
}
