package refautoappend_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refautoappend"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
)

func TestOpener_upsertsPendingLabel(t *testing.T) {
	t.Parallel()
	o := refautoappend.NewSessionOpener()
	if o.ID() != refautoappend.ID+"-session-open" {
		t.Fatalf("id %q", o.ID())
	}
	got, err := o.Open(context.Background(), session.OpenInput{
		TraceID: "t1",
		Session: session.SessionView{ClientSessionHint: "s1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionLabelUpserts[refautoappend.LabelKey] != refautoappend.LabelPending {
		t.Fatalf("labels %+v", got.SessionLabelUpserts)
	}
}

func TestRequestTransform_appendsOnNewSessionWithLabel(t *testing.T) {
	t.Parallel()
	rtx := refautoappend.NewRequestTransform(refautoappend.Config{FileText: " FROM_FILE"})
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{{
				Kind: lipapi.PartText,
				Text: "hello",
			}},
		}},
	}
	meta := request.RequestMeta{
		Session: session.SessionView{
			IsNew:  true,
			Labels: map[string]string{refautoappend.LabelKey: refautoappend.LabelPending},
		},
	}
	if err := extensions.RunRequestTransformStage(context.Background(), nil, nil, []request.Transform{rtx}, &call, meta, request.Services{
		State: state.DisabledStore{},
	}); err != nil {
		t.Fatal(err)
	}
	if call.Messages[0].Parts[0].Text != "hello FROM_FILE" {
		t.Fatalf("got %q", call.Messages[0].Parts[0].Text)
	}
}

func TestRequestTransform_noOpWhenNotNew(t *testing.T) {
	t.Parallel()
	rtx := refautoappend.NewRequestTransform(refautoappend.Config{FileText: "X"})
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}},
		}},
	}
	meta := request.RequestMeta{
		Session: session.SessionView{
			IsNew:  false,
			Labels: map[string]string{refautoappend.LabelKey: refautoappend.LabelPending},
		},
	}
	if err := extensions.RunRequestTransformStage(context.Background(), nil, nil, []request.Transform{rtx}, &call, meta, request.Services{
		State: state.DisabledStore{},
	}); err != nil {
		t.Fatal(err)
	}
	if call.Messages[0].Parts[0].Text != "hi" {
		t.Fatalf("mutated: %q", call.Messages[0].Parts[0].Text)
	}
}

func TestRequestTransform_noOpWhenLabelMissing(t *testing.T) {
	t.Parallel()
	rtx := refautoappend.NewRequestTransform(refautoappend.Config{FileText: "X"})
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}},
		}},
	}
	meta := request.RequestMeta{Session: session.SessionView{IsNew: true, Labels: map[string]string{}}}
	if err := extensions.RunRequestTransformStage(context.Background(), nil, nil, []request.Transform{rtx}, &call, meta, request.Services{
		State: state.DisabledStore{},
	}); err != nil {
		t.Fatal(err)
	}
	if call.Messages[0].Parts[0].Text != "hi" {
		t.Fatalf("mutated: %q", call.Messages[0].Parts[0].Text)
	}
}
