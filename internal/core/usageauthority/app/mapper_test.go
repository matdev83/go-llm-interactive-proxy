package app

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

// TestResolveAuthoritySource locks the priority logic of the projector-side
// authority source resolver. The four branches must be applied in order:
//
//  1. Settlement state in {Settled, Overage, Adjusted} → Reconciled.
//  2. Outcome=Unavailable AND SettlementState=Unavailable → Reconciled
//     (captures the unavailable-failure path that is still a final
//     reconciliation, not a reserved admission).
//  3. reserved && ReservationID != "" → Reserved.
//  4. Fallback: derive the authority source from the readiness status
//     (AdvisoryOnly→Advisory, Unavailable→Unavailable, Ready→Authoritative,
//     default→Estimated). The "reserved but empty reservationID" case
//     lands here as a status-state outcome, never as Reserved.
func TestResolveAuthoritySource(t *testing.T) {
	t.Parallel()

	ready := domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	advisory := domain.AuthorityStatus{State: domain.AuthorityStateAdvisoryOnly, Reason: domain.StatusReasonAdvisoryOnly}
	unavailable := domain.AuthorityStatus{State: domain.AuthorityStateUnavailable, Reason: domain.StatusReasonBackingUnavailable}
	degraded := domain.AuthorityStatus{State: domain.AuthorityStateDegraded, Reason: domain.StatusReasonBackingDegraded}

	cases := []struct {
		name     string
		status   domain.AuthorityStatus
		reserved bool
		in       Evidence
		want     controlplane.AccountingAuthoritySource
	}{
		// Branch 1: Settled|Overage|Adjusted → Reconciled, priority over everything else.
		{
			name:     "Settled takes priority over reserved+resID",
			status:   ready,
			reserved: true,
			in: Evidence{
				ReservationID:   "reservation-1",
				SettlementState: controlplane.AccountingSettlementSettled,
			},
			want: controlplane.AccountingAuthoritySourceReconciled,
		},
		{
			name:   "Overage → Reconciled",
			status: ready,
			in: Evidence{
				SettlementState: controlplane.AccountingSettlementOverage,
			},
			want: controlplane.AccountingAuthoritySourceReconciled,
		},
		{
			name:   "Adjusted → Reconciled",
			status: ready,
			in: Evidence{
				SettlementState: controlplane.AccountingSettlementAdjusted,
			},
			want: controlplane.AccountingAuthoritySourceReconciled,
		},
		// Branch 2: Outcome=Unavailable AND SettlementState=Unavailable → Reconciled.
		{
			name:   "Unavailable outcome + Unavailable settlement → Reconciled",
			status: ready,
			in: Evidence{
				Outcome:         controlplane.AccountingOutcomeUnavailable,
				SettlementState: controlplane.AccountingSettlementUnavailable,
			},
			want: controlplane.AccountingAuthoritySourceReconciled,
		},
		{
			name:   "Unavailable outcome alone (no Unavailable settlement) falls through",
			status: ready,
			in: Evidence{
				Outcome:         controlplane.AccountingOutcomeUnavailable,
				SettlementState: controlplane.AccountingSettlementPending,
			},
			want: controlplane.AccountingAuthoritySourceAuthoritative,
		},
		// Branch 3: reserved && ReservationID != "" → Reserved.
		{
			name:     "Pending + reserved + resID → Reserved",
			status:   ready,
			reserved: true,
			in: Evidence{
				ReservationID:   "reservation-1",
				SettlementState: controlplane.AccountingSettlementPending,
			},
			want: controlplane.AccountingAuthoritySourceReserved,
		},
		{
			name:     "Released settlement + reserved + resID → Reserved (SettlementState not in priority set)",
			status:   ready,
			reserved: true,
			in: Evidence{
				ReservationID:   "reservation-1",
				SettlementState: controlplane.AccountingSettlementReleased,
			},
			want: controlplane.AccountingAuthoritySourceReserved,
		},
		// Branch 4: reserved but no reservationID → status-state, never Reserved.
		{
			name:     "reserved + empty resID + Ready → Authoritative",
			status:   ready,
			reserved: true,
			in: Evidence{
				ReservationID:   "",
				SettlementState: controlplane.AccountingSettlementPending,
			},
			want: controlplane.AccountingAuthoritySourceAuthoritative,
		},
		{
			name:     "reserved + empty resID + AdvisoryOnly → Advisory",
			status:   advisory,
			reserved: true,
			in:       Evidence{ReservationID: ""},
			want:     controlplane.AccountingAuthoritySourceAdvisory,
		},
		{
			name:     "not reserved + Unavailable status → Unavailable",
			status:   unavailable,
			reserved: false,
			in:       Evidence{},
			want:     controlplane.AccountingAuthoritySourceUnavailable,
		},
		{
			name:     "not reserved + Degraded status → Estimated (default)",
			status:   degraded,
			reserved: false,
			in:       Evidence{},
			want:     controlplane.AccountingAuthoritySourceEstimated,
		},
		{
			name:   "not reserved + empty settlement + Ready → Authoritative",
			status: ready,
			in:     Evidence{SettlementState: ""},
			want:   controlplane.AccountingAuthoritySourceAuthoritative,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resolveAuthoritySource(tc.status, tc.reserved, tc.in)
			if got != tc.want {
				t.Fatalf("resolveAuthoritySource = %v, want %v (status=%+v reserved=%v in=%+v)",
					got, tc.want, tc.status, tc.reserved, tc.in)
			}
		})
	}
}

// TestResolveAuthoritySourceReasonCodeSpotCheck is a small companion test
// that confirms the projector's accounting reason code annotations stay in
// sync with the authority source branches above. It exists to catch
// accidental drift between the authority source and the policydecision
// reason code, which are the two safe-to-surface annotations a downstream
// observer compares when auditing a decision.
func TestResolveAuthoritySourceReasonCodeSpotCheck(t *testing.T) {
	t.Parallel()

	ready := domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}

	cases := []struct {
		name string
		in   Evidence
		want controlplane.AccountingAuthoritySource
	}{
		{
			name: "Reserved admission",
			in: Evidence{
				Outcome:         controlplane.AccountingOutcomeReserve,
				ReasonCode:      policydecision.AccountingReasonReserved,
				ReservationID:   "reservation-1",
				SettlementState: controlplane.AccountingSettlementPending,
			},
			want: controlplane.AccountingAuthoritySourceReserved,
		},
		{
			name: "Reconciled settlement",
			in: Evidence{
				Outcome:         controlplane.AccountingOutcomeReconcile,
				ReasonCode:      policydecision.AccountingReasonReconciled,
				ReservationID:   "reservation-1",
				SettlementState: controlplane.AccountingSettlementSettled,
			},
			want: controlplane.AccountingAuthoritySourceReconciled,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resolveAuthoritySource(ready, true, tc.in)
			if got != tc.want {
				t.Fatalf("resolveAuthoritySource = %v, want %v", got, tc.want)
			}
		})
	}
}
