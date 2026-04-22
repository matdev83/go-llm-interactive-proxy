package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	lipruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestReplayLineage_recvFailoverIncrementsBLegs(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := &lipruntime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  rand.New(rand.NewSource(1)),
		Backends: map[string]lipruntime.Backend{
			"bad": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					return &oneThenFailStream{
						first: lipapi.Event{Kind: lipapi.EventResponseStarted},
						then:  lipapi.RecoverablePreOutputError(errors.New("recv")),
					}, nil
				},
			},
			"ok": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "lineage-recv"},
		Route:   lipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	leg, err := st.ResolveALeg(context.Background(), "lineage-recv")
	if err != nil {
		t.Fatal(err)
	}
	rowsBefore, _ := st.LoadAttempts(context.Background(), leg.ALegID)
	for {
		_, err := s.Recv(context.Background())
		if err != nil {
			break
		}
	}
	_ = s.Close()
	rowsAfter, err := st.LoadAttempts(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rowsAfter) <= len(rowsBefore) {
		t.Fatalf("expected attempt rows after recv failover, before=%d after=%d", len(rowsBefore), len(rowsAfter))
	}
}

type oneThenFailStream struct {
	sent  bool
	first lipapi.Event
	then  error
}

func (o *oneThenFailStream) Recv(context.Context) (lipapi.Event, error) {
	if !o.sent {
		o.sent = true
		return o.first, nil
	}
	return lipapi.Event{}, o.then
}

func (o *oneThenFailStream) Close() error { return nil }

func TestReplayFixture_minimalRequestJSON(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	p := filepath.Join(repoRoot, "testdata", "replay", "minimal_request.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["user_message"] == nil {
		t.Fatalf("fixture: %#v", got)
	}
}
