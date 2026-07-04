package controlplane_test

import (
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func validBaseEvent() cp.Event {
	return cp.Event{
		Category:       cp.CategoryAuth,
		OccurredAt:     time.Now(),
		RecordedAt:     time.Now(),
		Visibility:     cp.VisibilityDefault,
		EvidenceState:  cp.EvidenceRecorded,
		RedactionState: cp.RedactionNone,
		Auth:           &cp.AuthDetail{Outcome: "allow"},
		Source:         cp.SourceRef{Name: "authsink"},
	}
}

func TestValidateEventAcceptsValid(t *testing.T) {
	t.Parallel()
	if err := controlplane.ValidateEvent(validBaseEvent()); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
}

func TestValidateEventRejectsProblems(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mut  func(cp.Event) cp.Event
		want string
	}{
		{
			name: "unknown_category",
			mut:  func(e cp.Event) cp.Event { e.Category = cp.Category("bogus"); return e },
			want: "unknown category",
		},
		{
			name: "zero_occurred_at",
			mut:  func(e cp.Event) cp.Event { e.OccurredAt = time.Time{}; return e },
			want: "occurred_at",
		},
		{
			name: "recorded_before_occurred",
			mut: func(e cp.Event) cp.Event {
				e.OccurredAt = time.Now()
				e.RecordedAt = e.OccurredAt.Add(-time.Minute)
				return e
			},
			want: "recorded_at precedes occurred_at",
		},
		{
			name: "unknown_visibility",
			mut:  func(e cp.Event) cp.Event { e.Visibility = cp.Visibility("bogus"); return e },
			want: "unknown visibility",
		},
		{
			name: "unknown_evidence_state",
			mut:  func(e cp.Event) cp.Event { e.EvidenceState = cp.EvidenceState("bogus"); return e },
			want: "unknown evidence state",
		},
		{
			name: "unknown_redaction_state",
			mut:  func(e cp.Event) cp.Event { e.RedactionState = cp.RedactionState("bogus"); return e },
			want: "unknown redaction state",
		},
		{
			name: "privileged_visibility_without_privileged_redaction",
			mut: func(e cp.Event) cp.Event {
				e.Visibility = cp.VisibilityPrivileged
				e.RedactionState = cp.RedactionNone
				return e
			},
			want: "privileged",
		},
		{
			name: "empty_source_name",
			mut:  func(e cp.Event) cp.Event { e.Source = cp.SourceRef{}; return e },
			want: "source",
		},
		{
			name: "oversized_summary",
			mut:  func(e cp.Event) cp.Event { e.Summary = strings.Repeat("x", controlplane.MaxSummaryBytes+1); return e },
			want: "summary",
		},
		{
			name: "oversized_scope_labels",
			mut: func(e cp.Event) cp.Event {
				labels := make(map[string]string, controlplane.MaxScopeMapEntries+1)
				for i := range controlplane.MaxScopeMapEntries + 1 {
					labels["k"+itoa(i)] = "v"
				}
				e.Scope = cp.ScopeSnapshot{
					Principal: scope.PrincipalScopeView{PolicyLabels: labels},
				}
				return e
			},
			want: "scope",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := controlplane.ValidateEvent(c.mut(validBaseEvent()))
			if err == nil {
				t.Fatalf("invalid event accepted: %s", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q must contain %q", err.Error(), c.want)
			}
		})
	}
}

func TestValidateEventRejectsUnsafeEvidence(t *testing.T) {
	t.Parallel()
	// an event carrying a raw-token-looking summary must be rejected as unsafe
	ev := validBaseEvent()
	ev.Summary = "Bearer abcdef"
	if err := controlplane.ValidateEvent(ev); err == nil {
		t.Fatalf("unsafe bearer-bearing summary must be rejected")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
