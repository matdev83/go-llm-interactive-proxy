package app

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

func TestTranslateAuthorityStatus(t *testing.T) {
	t.Parallel()

	now := time.Unix(123, 0).UTC()
	cases := []struct {
		name string
		in   domain.AuthorityStatus
		want controlplane.AccountingAuthorityStatus
	}{
		{
			name: "disabled",
			in:   domain.AuthorityStatus{State: domain.AuthorityStateDisabled, Reason: domain.StatusReasonDisabledByConfig},
			want: controlplane.AccountingAuthorityStatus{
				State:          controlplane.AccountingAuthorityDisabled,
				Reason:         controlplane.ReasonDisabled,
				LastUpdatedAt:  now,
				EvidenceState:  controlplane.EvidenceRecorded,
				RedactionState: controlplane.RedactionSummarized,
			},
		},
		{
			name: "ready",
			in:   domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			want: controlplane.AccountingAuthorityStatus{
				State:          controlplane.AccountingAuthorityReady,
				Reason:         controlplane.ReasonNone,
				LastUpdatedAt:  now,
				EvidenceState:  controlplane.EvidenceRecorded,
				RedactionState: controlplane.RedactionSummarized,
			},
		},
		{
			name: "degraded",
			in:   domain.AuthorityStatus{State: domain.AuthorityStateDegraded, Reason: domain.StatusReasonBackingDegraded},
			want: controlplane.AccountingAuthorityStatus{
				State:          controlplane.AccountingAuthorityDegraded,
				Reason:         controlplane.ReasonStoreNotReady,
				LastUpdatedAt:  now,
				EvidenceState:  controlplane.EvidenceRecorded,
				RedactionState: controlplane.RedactionSummarized,
			},
		},
		{
			name: "unavailable",
			in:   domain.AuthorityStatus{State: domain.AuthorityStateUnavailable, Reason: domain.StatusReasonBackingUnavailable},
			want: controlplane.AccountingAuthorityStatus{
				State:          controlplane.AccountingAuthorityUnavailable,
				Reason:         controlplane.ReasonBackingUnavailable,
				LastUpdatedAt:  now,
				EvidenceState:  controlplane.EvidenceRecorded,
				RedactionState: controlplane.RedactionSummarized,
			},
		},
		{
			name: "advisory-only",
			in:   domain.AuthorityStatus{State: domain.AuthorityStateAdvisoryOnly, Reason: domain.StatusReasonAdvisoryOnly},
			want: controlplane.AccountingAuthorityStatus{
				State:          controlplane.AccountingAuthorityAdvisoryOnly,
				Reason:         controlplane.ReasonUnsupported,
				LastUpdatedAt:  now,
				EvidenceState:  controlplane.EvidenceRecorded,
				RedactionState: controlplane.RedactionSummarized,
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := TranslateAuthorityStatus(tt.in, now)
			if got.State != tt.want.State || got.Reason != tt.want.Reason {
				t.Fatalf("status translation mismatch: got %#v want %#v", got, tt.want)
			}
			if !got.LastUpdatedAt.Equal(tt.want.LastUpdatedAt) {
				t.Fatalf("timestamp translation mismatch: got %v want %v", got.LastUpdatedAt, tt.want.LastUpdatedAt)
			}
			if got.EvidenceState != tt.want.EvidenceState || got.RedactionState != tt.want.RedactionState {
				t.Fatalf("status safety metadata mismatch: got %#v want %#v", got, tt.want)
			}
		})
	}
}
