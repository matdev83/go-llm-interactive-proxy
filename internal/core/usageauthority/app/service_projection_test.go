package app

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

func TestProjectAccountingEvidence(t *testing.T) {
	t.Parallel()

	now := time.Unix(123, 0).UTC()
	status := domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	input := Evidence{
		At:              now,
		Correlation:     controlplane.Correlation{TraceID: "trace-1", RequestID: "request-1", ALegID: "a-1", BLegID: "b-1", AttemptSeq: 3, BackendID: "backend-1", Model: "model-1"},
		Scope:           principalScope(),
		RuleID:          "tenant.requests",
		RuleType:        "quota",
		Outcome:         controlplane.AccountingOutcomeReserve,
		ReasonCode:      policydecision.AccountingReasonReserved,
		ReservationID:   "reservation-1",
		SettlementState: controlplane.AccountingSettlementPending,
		Unit:            "requests",
		Limit:           10,
		Consumed:        6,
		Reserved:        4,
		Adjustment:      -1,
	}

	record, ok := ProjectPolicyDecision(status, true, input)
	if !ok {
		t.Fatalf("policydecision projection must succeed")
	}
	if record.Stage != feature.StageIDPreRequest {
		t.Fatalf("projected policy decision must use a legal stage: %#v", record)
	}
	if record.Annotations["accounting.rule_id"] != "tenant.requests" {
		t.Fatalf("rule annotation lost: %#v", record.Annotations)
	}
	if record.Annotations["accounting.reason"] != string(policydecision.AccountingReasonReserved) {
		t.Fatalf("reason annotation lost: %#v", record.Annotations)
	}
	if record.Annotations["accounting.reservation_id"] != "reservation-1" {
		t.Fatalf("reservation annotation lost: %#v", record.Annotations)
	}
	if err := policydecision.ValidateRecord(record); err != nil {
		t.Fatalf("projected policydecision must stay legal: %v", err)
	}

	ev, err := ProjectAccountingAuthorityEvent(status, true, input)
	if err != nil {
		t.Fatalf("accounting authority event projection: %v", err)
	}
	if ev.Category != controlplane.CategoryAccountingAuthority || ev.AccountingAuthority == nil {
		t.Fatalf("projection must produce accounting authority event: %#v", ev)
	}
	if ev.Source.Name != "usageauthority" || ev.Source.Version != "app" {
		t.Fatalf("source metadata lost: %#v", ev.Source)
	}
	// Authority is derived from (status, reserved, in) by the projector; it
	// must agree with resolveAuthoritySource's reserved-with-id branch.
	if ev.AccountingAuthority.Authority != controlplane.AccountingAuthoritySourceReserved {
		t.Fatalf("authority derivation must honor reserved+reservationID: got %v", ev.AccountingAuthority.Authority)
	}
	// WindowStart/End/ResetAt are left as the time.Time zero value to signal
	// "no window" — the rule snapshot, not this projector, owns window metadata.
	// Defaulting them to in.At would read as a zero-length window that resets
	// now, which is misleading for unbounded or admission-only decisions.
	if !ev.AccountingAuthority.WindowStart.IsZero() ||
		!ev.AccountingAuthority.WindowEnd.IsZero() ||
		!ev.AccountingAuthority.WindowResetAt.IsZero() {
		t.Fatalf("window fields must be left as the zero value to signal no window: got start=%v end=%v reset=%v", ev.AccountingAuthority.WindowStart, ev.AccountingAuthority.WindowEnd, ev.AccountingAuthority.WindowResetAt)
	}
	if ev.SourceEventKey == "" {
		t.Fatalf("projection must produce a stable source key")
	}
	if err := ev.Validate(); err != nil {
		t.Fatalf("projected controlplane event must be legal: %v", err)
	}
}
