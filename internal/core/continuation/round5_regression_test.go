package continuation_test

import (
	"context"
	"errors"
	"testing"

	corecont "github.com/matdev83/go-llm-interactive-proxy/internal/core/continuation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

func TestRound5MemoryStoreRejectsUnprotectedNativeReferences(t *testing.T) {
	store := corecont.NewMemoryStore()
	scope := lipcont.Scope{PrincipalID: "round5-principal"}
	id, err := store.Reserve(context.Background(), scope, lipcont.StoragePolicy{Mode: lipcont.PersistencePersistent})
	if err != nil {
		t.Fatal(err)
	}
	err = store.PutTerminal(context.Background(), lipcont.ContinuationRecord{
		ID:         id,
		Scope:      scope,
		Terminal:   true,
		NativeRefs: []lipcont.NativeReference{{Provider: "provider", ID: "native"}},
	})
	if !errors.Is(err, lipcont.ErrNativeReferencesUnprotected) {
		t.Fatalf("PutTerminal error = %v, want ErrNativeReferencesUnprotected", err)
	}
}

type round5Recorder struct{ calls int }

func (r *round5Recorder) RecordTerminal(context.Context, lipcont.ContinuationRecord) error {
	r.calls++
	return nil
}

func TestRound5RecorderCountsReasoningBytesAgainstQuota(t *testing.T) {
	backend := &round5Recorder{}
	recorder := corecont.NewStreamRecorder(backend, lipcont.ContinuationRecord{
		Policy: lipcont.StoragePolicy{Limits: lipcont.StorageLimits{MaxRecordBytes: 5}},
	}, func() {})
	recorder.Observe(context.Background(), lipapi.Event{
		Kind: lipapi.EventReasoningPart,
		Reasoning: &lipapi.ReasoningPart{
			Text:      "reasoning",
			Signature: "sig",
			Opaque:    []byte("opaque"),
		},
	})
	if backend.calls != 0 {
		t.Fatal("reasoning event exceeded quota but was persisted")
	}
}
