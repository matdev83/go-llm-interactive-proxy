package openaicodex

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaicodex"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// TestWSContinuationRotation_preservesFullPayloadAcrossAccounts is a regression
// repro for the managed-OAuth WebSocket account-rotation path reported by Bugbot.
//
// When the first account's WS handshake returns 401/403/429, openWSPreparedAttemptOnce
// clears the prepared continuation entry but does NOT restore env.payload from the
// fullPayload snapshot (unlike the write/read/previous-response retry paths, which
// all do env.payload = fullPayload). Because openManagedAccountLoop reuses a single
// *codexOpenEnv across account retries, the next account is dialed with a
// continuation-trimmed Input and a PreviousResponseID that belongs to the first
// account.
//
// This test asserts the correct behavior (full input, no foreign previous_response_id
// on the rotated account) and is expected to FAIL on the current code, proving the
// finding is not a false positive.
func TestWSContinuationRotation_preservesFullPayloadAcrossAccounts(t *testing.T) {
	t.Parallel()

	// Two managed accounts. The refbackend is configured with account B's token, so
	// account A's WS handshake returns 401 and openManagedAccountLoop rotates to B.
	dir := t.TempDir()
	accountFiles := []struct{ name, id, token string }{
		{"a.json", "acct-a", "tok-a"},
		{"b.json", "acct-b", "tok-b"},
	}
	for _, af := range accountFiles {
		path := filepath.Join(dir, af.name)
		if err := os.WriteFile(path, []byte(`{"account_id":"`+af.id+`","access_token":"`+af.token+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := newAccountStore(Config{
		ManagedOAuthStoragePath:       dir,
		ManagedOAuthSelectionStrategy: "first-available",
		RateLimitFallback:             time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := refbackend.New(refbackend.Config{Token: "tok-b", OutputText: "ws-ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	cfg := Config{
		BaseURL:    ts.URL + "/backend-api/codex",
		HTTPClient: ts.Client(),
		Transport:  TransportWebSocket,
	}
	policy := newDowngradePolicy(cfg)

	call := lipapi.Call{
		ID:      "repro-rotation-call",
		Session: lipapi.SessionRef{ContinuityKey: "conv-repro"},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("inspect")}},
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("continue")}},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex-spark"}}

	// Reconstruct the exact payload openManagedAccountLoop will build, so the seeded
	// continuation entry for account A has matching Instructions/Tools/PromptCacheKey
	// fingerprints and is actually applied on the first attempt.
	seedEnv, err := prepareCodexOpenEnv(context.Background(), &cfg, call, cand, policy)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(seedEnv.payload.Input); got != 2 {
		t.Fatalf("seed payload input len = %d, want 2 (inspect + continue)", got)
	}
	basePayload := seedEnv.payload
	basePayload.Input = append([]inputItem(nil), seedEnv.payload.Input[:1]...) // [user "inspect"]

	cfgA := cfg
	cfgA.AccountID = "acct-a"

	// Sanity check on a throwaway store: the seeded entry must be applied for account A,
	// trimming Input and setting PreviousResponseID. This proves the rotation path
	// actually enters the continuation branch before the 401 handshake.
	checkStore := newWSContinuationStore(time.Minute, 8)
	checkStore.record(&cfgA, call, basePayload, "resp_a")
	checkPayload := seedEnv.payload
	if !checkStore.prepareWithFingerprints(context.Background(), &cfgA, call, &checkPayload, seedEnv.inputFingerprints) {
		t.Fatal("seeded continuation entry was not applied for account A; repro setup is broken")
	}
	if checkPayload.PreviousResponseID != "resp_a" || len(checkPayload.Input) != 1 {
		t.Fatalf("sanity check: continuation did not trim payload: prev=%q input=%#v",
			checkPayload.PreviousResponseID, checkPayload.Input)
	}

	// Real run: seed the continuation store used by openManagedWS.
	continuation := newWSContinuationStore(time.Minute, 8)
	continuation.record(&cfgA, call, basePayload, "resp_a")

	es, err := openManagedWS(context.Background(), &cfg, store, call, cand, policy, nil, newWSSessionStore(), continuation)
	if err != nil {
		t.Fatalf("openManagedWS: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	// The refbackend captures the response.create frame before sending any event, so
	// the first Recv guarantees the wire payload is recorded.
	if _, err := es.Recv(context.Background()); err != nil {
		t.Fatalf("first Recv: %v", err)
	}

	captured := srv.LatestRequest()
	if captured.Transport != "websocket" {
		t.Fatalf("captured transport = %q, want websocket", captured.Transport)
	}
	if got := captured.Authorization; got != "Bearer tok-b" {
		t.Fatalf("captured authorization = %q, want %q (rotation must reach account B)", got, "Bearer tok-b")
	}

	input, _ := captured.Body["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("rotated account B received input len = %d, want 2 (full payload must be sent to the fresh account); input=%#v",
			len(input), input)
	}
	first, _ := input[0].(map[string]any)
	if first["content"] != "inspect" {
		t.Fatalf("rotated account B first input content = %v, want %q (continuation-trimmed payload leaked from account A)",
			first["content"], "inspect")
	}

	if prev, ok := captured.Body["previous_response_id"]; ok {
		t.Fatalf("rotated account B received previous_response_id = %v; continuation state from account A must not leak across accounts",
			prev)
	}
}
