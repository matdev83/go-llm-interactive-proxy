package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestProjectAuthorityEvidenceUsesBoundedSafeFields(t *testing.T) {
	t.Parallel()

	now := time.Unix(123, 0).UTC()
	input := Evidence{
		At: now,
		Correlation: controlplane.Correlation{
			TraceID:    "trace-1",
			RequestID:  "request-1",
			ALegID:     "a-1",
			BLegID:     "b-1",
			AttemptSeq: 3,
			BackendID:  "backend-1",
			Model:      "model-1",
		},
		Scope:           scope.PrincipalScopeView{PrincipalID: scope.Known("principal-1"), TenantID: scope.Known("tenant-1")},
		RuleID:          "tenant.requests",
		RuleType:        "quota",
		Outcome:         controlplane.AccountingOutcomeReserve,
		ReasonCode:      policydecision.AccountingReasonReserved,
		ReservationID:   "reservation-1",
		SettlementState: controlplane.AccountingSettlementSettled,
		Unit:            "requests",
		Limit:           10,
		Consumed:        6,
		Reserved:        4,
		Adjustment:      -1,
	}

	status := domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}

	projected, err := ProjectAccountingAuthorityEvent(status, true, input)
	if err != nil {
		t.Fatalf("ProjectAccountingAuthorityEvent: %v", err)
	}
	raw, err := json.Marshal(projected)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	assertNoUnsafeAuthorityMarkers(t, string(raw))

	record, ok := ProjectPolicyDecision(status, true, input)
	if !ok {
		t.Fatal("ProjectPolicyDecision must succeed")
	}
	policyRaw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal policydecision: %v", err)
	}
	assertNoUnsafeAuthorityMarkers(t, string(policyRaw))
}

func TestProjectAuthorityEvidenceRejectsUnsafeEnumEchoes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		mut  func(*Evidence)
	}{
		{
			name: "outcome",
			mut: func(in *Evidence) {
				in.Outcome = controlplane.AccountingOutcome("Bearer abc123")
			},
		},
		{
			name: "settlement_state",
			mut: func(in *Evidence) {
				in.SettlementState = controlplane.AccountingSettlementState("x-api-key")
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			in := Evidence{
				At:          time.Unix(123, 0).UTC(),
				Correlation: controlplane.Correlation{TraceID: "trace-1", RequestID: "request-1"},
				Scope:       scope.PrincipalScopeView{},
				RuleID:      "tenant.requests",
				RuleType:    "quota",
				ReasonCode:  policydecision.AccountingReasonReserved,
				Unit:        "requests",
				Limit:       10,
			}
			tt.mut(&in)

			_, err := ProjectAccountingAuthorityEvent(domain.AuthorityStatus{State: domain.AuthorityStateReady}, false, in)
			if err == nil {
				t.Fatal("expected invalid evidence to fail")
			}
			if got := strings.ToLower(err.Error()); strings.Contains(got, "bearer abc123") || strings.Contains(got, "x-api-key") {
				t.Fatalf("invalid evidence error leaked raw input: %v", err)
			}
		})
	}
}

func assertNoUnsafeAuthorityMarkers(t *testing.T, body string) {
	t.Helper()

	low := strings.ToLower(body)
	for _, bad := range []string{
		"bearer ",
		"api key",
		"api-key",
		"oauth",
		"resume token",
		"resume_token",
		"prompt",
		"response",
		"provider payload",
		"raw payload",
		"raw body",
		"raw headers",
		"authorization:",
		"sql",
		"driver",
		"dsn",
	} {
		if strings.Contains(low, bad) {
			t.Fatalf("authority evidence leaked forbidden substring %q in %s", bad, body)
		}
	}
}
