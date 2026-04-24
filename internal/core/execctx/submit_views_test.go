package execctx_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestViewsFromSubmit_sessionHintsAndAuthoritative(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	aLeg := b2bua.ALegRecord{
		ALegID:        "aleg-1",
		ContinuityKey: "ck",
		CreatedAt:     now,
		LastSeenAt:    now,
	}
	t.Run("client hint only", func(t *testing.T) {
		t.Parallel()
		call := lipapi.Call{
			Session: lipapi.SessionRef{ClientSessionID: "client-sess"},
		}
		v := execctx.ViewsFromSubmit("trace-1", aLeg, call, map[string]string{"k": "v"})
		if v.Session.AuthoritativeSessionID != "" {
			t.Fatalf("authoritative: %q", v.Session.AuthoritativeSessionID)
		}
		if v.Session.ClientSessionHint != "client-sess" {
			t.Fatalf("hint: %q", v.Session.ClientSessionHint)
		}
		if v.Session.ALegID != "aleg-1" {
			t.Fatalf("aleg: %q", v.Session.ALegID)
		}
		if !v.Session.IsNew {
			t.Fatal("want IsNew when CreatedAt equals LastSeenAt")
		}
		if v.Session.PartitionKey() != "client-sess" {
			t.Fatalf("partition: %q", v.Session.PartitionKey())
		}
		if v.Attempt.TraceID != "trace-1" {
			t.Fatalf("attempt trace: %q", v.Attempt.TraceID)
		}
		if v.Annotations["k"] != "v" {
			t.Fatalf("annotations: %v", v.Annotations)
		}
	})
	t.Run("authoritative from call", func(t *testing.T) {
		t.Parallel()
		secret := "RAW_RESUME_BEARER_SHOULD_NOT_APPEAR_IN_VIEW"
		call := lipapi.Call{
			Session: lipapi.SessionRef{
				ClientSessionID:        "client-sess",
				AuthoritativeSessionID: "proxy-auth-sess",
				ResumeToken:            secret,
			},
		}
		v := execctx.ViewsFromSubmit("trace-2", aLeg, call, nil)
		if v.Session.AuthoritativeSessionID != "proxy-auth-sess" {
			t.Fatalf("authoritative: %q", v.Session.AuthoritativeSessionID)
		}
		if v.Session.ClientSessionHint != "client-sess" {
			t.Fatalf("hint: %q", v.Session.ClientSessionHint)
		}
		if strings.Contains(fmt.Sprintf("%+v", v.Session), secret) {
			t.Fatal("resume token leaked into stringified view")
		}
		if v.Session.PartitionKey() != "proxy-auth-sess" {
			t.Fatalf("partition: %q", v.Session.PartitionKey())
		}
	})
}

func TestViewsFromSecureSubmit_authoritativeTurnAndPolicyLabels(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	aLeg := b2bua.ALegRecord{
		ALegID:        "aleg-sec",
		ContinuityKey: "",
		CreatedAt:     now,
		LastSeenAt:    now.Add(time.Minute),
	}
	call := lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID:        "client-hint",
			AuthoritativeSessionID: "wrong-client-authority",
			ResumeToken:            "raw-should-not-leak",
		},
	}
	v := execctx.ViewsFromSecureSubmit(execctx.SecureSubmitViewsInput{
		TraceID:                "tr-sec",
		ALeg:                   aLeg,
		Call:                   call,
		AuthoritativeSessionID: "proxy-owned-sid",
		TurnID:                 "turn-zz",
		ResumeEligible:         true,
		PolicyLabels:           map[string]string{"effective_treatment": "strict"},
	})
	if v.Session.AuthoritativeSessionID != "proxy-owned-sid" {
		t.Fatalf("authoritative: %q", v.Session.AuthoritativeSessionID)
	}
	if v.Session.TurnID != "turn-zz" {
		t.Fatalf("turn: %q", v.Session.TurnID)
	}
	if !v.Session.ResumeEligible {
		t.Fatal("expected resume eligible")
	}
	if v.Session.Labels["effective_treatment"] != "strict" {
		t.Fatalf("labels: %v", v.Session.Labels)
	}
	if strings.Contains(fmt.Sprintf("%+v", v), "raw-should-not-leak") {
		t.Fatal("resume token leaked into view dump")
	}
}
