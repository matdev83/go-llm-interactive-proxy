package authoritycoord

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
)

// pushRequestDecisionHolds registers reservation handles from d for later
// reverse compensation via ReleaseRequest.
func pushRequestDecisionHolds(
	stack *CompensationStack,
	providerID string,
	prov authority.RequestProvider,
	reqID string,
	d authority.Decision,
) {
	if stack == nil || prov == nil {
		return
	}
	for _, r := range d.Reservations {
		handle := strings.TrimSpace(r.Handle)
		if handle == "" {
			handle = strings.TrimSpace(d.CompensationHandle)
		}
		if handle == "" {
			continue
		}
		h := handle
		res := r
		stack.Push(StackEntry{
			ProviderID:  providerID,
			Handle:      h,
			Reservation: res,
			Evidence:    d.Evidence,
			Compensate: func(cctx context.Context) error {
				return prov.ReleaseRequest(cctx, authority.RequestRelease{
					RequestID:          reqID,
					Handles:            []string{h},
					CompensationHandle: h,
				})
			},
		})
	}
}

// pushAttemptDecisionHolds registers reservation handles from d for later
// reverse compensation via ReleaseAttempt.
func pushAttemptDecisionHolds(
	stack *CompensationStack,
	providerID string,
	prov authority.AttemptProvider,
	reqID, attID, bleg string,
	d authority.Decision,
) {
	if stack == nil || prov == nil {
		return
	}
	for _, r := range d.Reservations {
		handle := strings.TrimSpace(r.Handle)
		if handle == "" {
			handle = strings.TrimSpace(d.CompensationHandle)
		}
		if handle == "" {
			continue
		}
		h := handle
		res := r
		stack.Push(StackEntry{
			ProviderID:  providerID,
			Handle:      h,
			Reservation: res,
			Evidence:    d.Evidence,
			Compensate: func(cctx context.Context) error {
				return prov.ReleaseAttempt(cctx, authority.AttemptRelease{
					RequestID:          reqID,
					AttemptID:          attID,
					BLegID:             bleg,
					Handles:            []string{h},
					CompensationHandle: h,
				})
			},
		})
	}
}

// pushLeaseDecisionHolds registers lease IDs from ld for later reverse
// compensation via ReleaseLease.
func pushLeaseDecisionHolds(
	stack *CompensationStack,
	conc authority.ConcurrencyProvider,
	reqID string,
	ld authority.LeaseDecision,
) {
	if stack == nil || conc == nil {
		return
	}
	for _, id := range leaseIDsFromDecision(ld) {
		leaseID := id
		stack.Push(StackEntry{
			ProviderID: "concurrency",
			Handle:     leaseID,
			Compensate: func(cctx context.Context) error {
				return conc.ReleaseLease(cctx, authority.LeaseRelease{
					LeaseID:   leaseID,
					RequestID: reqID,
				})
			},
		})
	}
}
