package reasoningpreservation_test

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func mustYAML(t *testing.T, s string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(s), &n); err != nil {
		t.Fatalf("mustYAML: %v", err)
	}
	return n
}

func reasoningPart(dialect lipapi.ReasoningDialect, text, signature string, opaque json.RawMessage) lipapi.Part {
	var op json.RawMessage
	if len(opaque) > 0 {
		op = append(json.RawMessage(nil), opaque...)
	}
	return lipapi.Part{
		Kind: lipapi.PartReasoning,
		Reasoning: &lipapi.ReasoningPart{
			Dialect:   dialect,
			Text:      text,
			Signature: signature,
			Opaque:    op,
		},
	}
}

func assistantMsg(parts ...lipapi.Part) lipapi.Message {
	return lipapi.Message{
		Role:  lipapi.RoleAssistant,
		Parts: append([]lipapi.Part(nil), parts...),
	}
}

func redNotImplemented(t *testing.T, err error, msg string) {
	t.Helper()
	if errors.Is(err, reasoningpreservation.ErrNotImplemented) {
		t.Fatalf("RED: %s", msg)
	}
}

func newTestClock(t0 time.Time) (now func() time.Time, advance func(time.Duration)) {
	var mu sync.Mutex
	cur := t0
	return func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return cur
		}, func(d time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			cur = cur.Add(d)
		}
}

func mustOpaqueJSON(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	b := json.RawMessage(raw)
	if !json.Valid(b) {
		t.Fatalf("invalid JSON opaque: %q", raw)
	}
	return append(json.RawMessage(nil), b...)
}

func jsonPart(content string) lipapi.Part {
	return lipapi.Part{
		Kind:    lipapi.PartJSON,
		Content: json.RawMessage(content),
	}
}

func boolPtr(v bool) *bool {
	return new(v)
}

func turnArtifact(id string, anchor [32]byte, reasoning ...reasoningpreservation.PlacedReasoning) reasoningpreservation.TurnArtifact {
	return reasoningpreservation.TurnArtifact{
		ID:             id,
		Anchor:         anchor,
		SourceBackend:  "src-backend",
		SourceModel:    "src-model",
		Reasoning:      append([]reasoningpreservation.PlacedReasoning(nil), reasoning...),
		CreatedAt:      time.Unix(1_700_000_000, 0).UTC(),
		ReasoningBytes: 128,
	}
}

func placedReasoning(before int, part lipapi.Part) reasoningpreservation.PlacedReasoning {
	return reasoningpreservation.PlacedReasoning{
		BeforeNonReasoningPart: before,
		Part:                   part,
	}
}

func decodeValidConfig(t *testing.T, yamlBody string) reasoningpreservation.Config {
	t.Helper()
	cfg, err := reasoningpreservation.DecodeConfig(mustYAML(t, yamlBody))
	redNotImplemented(t, err, "DecodeConfig must be implemented")
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	return cfg
}

func decodeConfigExpectError(t *testing.T, yamlBody string) error {
	t.Helper()
	_, err := reasoningpreservation.DecodeConfig(mustYAML(t, yamlBody))
	redNotImplemented(t, err, "DecodeConfig validation must be implemented")
	return err
}

func newMemoryStore(t *testing.T, opts reasoningpreservation.StoreOptions) reasoningpreservation.TurnStore {
	t.Helper()
	st, err := reasoningpreservation.NewMemoryTurnStore(opts)
	redNotImplemented(t, err, "NewMemoryTurnStore must be implemented")
	if err != nil {
		t.Fatalf("NewMemoryTurnStore: %v", err)
	}
	return st
}
