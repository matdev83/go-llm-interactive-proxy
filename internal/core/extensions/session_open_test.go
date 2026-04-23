package extensions_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

type open1 struct{}

func (open1) ID() string { return "o1" }
func (open1) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{SessionLabelUpserts: map[string]string{"k": "v"}}, nil
}

type openErr struct{}

func (openErr) ID() string { return "err" }
func (openErr) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, context.Canceled
}

func TestRunSessionOpenStage_failOpenContinues(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		SessionOpeners: []session.Opener{openErr{}, open1{}},
	})
	in := session.OpenInput{TraceID: "t1", Session: session.SessionView{SessionID: "s"}}
	got := extensions.RunSessionOpenStage(context.Background(), nil, nil, snap.SessionOpeners(), in)
	if got.SessionLabelUpserts["k"] != "v" {
		t.Fatalf("labels %+v", got.SessionLabelUpserts)
	}
}
