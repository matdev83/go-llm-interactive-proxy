package authoritycoord

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
)

// Provider invoke helpers isolate panics and validate decision shape (req 15.9).

func invokeAdmitRequest(p authority.RequestProvider, ctx context.Context, in authority.RequestAdmission) (d authority.Decision, err error) {
	if p == nil {
		return authority.Decision{Kind: authority.DecisionAllow}, nil
	}
	defer isolateProviderPanic(&d, &err)
	d, err = p.AdmitRequest(ctx, in)
	if err != nil {
		return d, err
	}
	if vErr := d.Validate(); vErr != nil {
		// Keep d so callers can reverse-compensate any claimed holds (req 15.9).
		return d, vErr
	}
	return d, nil
}

func invokeAdmitAttempt(p authority.AttemptProvider, ctx context.Context, in authority.AttemptAdmission) (d authority.Decision, err error) {
	if p == nil {
		return authority.Decision{Kind: authority.DecisionAllow}, nil
	}
	defer isolateProviderPanic(&d, &err)
	d, err = p.AdmitAttempt(ctx, in)
	if err != nil {
		return d, err
	}
	if vErr := d.Validate(); vErr != nil {
		// Keep d so callers can reverse-compensate any claimed holds (req 15.9).
		return d, vErr
	}
	return d, nil
}

func invokeSettleRequest(p authority.RequestProvider, ctx context.Context, in authority.RequestSettlement) (s authority.Settlement, err error) {
	if p == nil {
		return authority.Settlement{Kind: authority.SettlementFinal}, nil
	}
	defer func() {
		if r := recover(); r != nil {
			s = authority.Settlement{}
			err = fmt.Errorf("authoritycoord: provider panic: %v", r)
		}
	}()
	return p.SettleRequest(ctx, in)
}

func invokeSettleAttempt(p authority.AttemptProvider, ctx context.Context, in authority.AttemptSettlement) (s authority.Settlement, err error) {
	if p == nil {
		return authority.Settlement{Kind: authority.SettlementFinal}, nil
	}
	defer func() {
		if r := recover(); r != nil {
			s = authority.Settlement{}
			err = fmt.Errorf("authoritycoord: provider panic: %v", r)
		}
	}()
	return p.SettleAttempt(ctx, in)
}

func invokeAdmitLease(p authority.ConcurrencyProvider, ctx context.Context, in authority.LeaseAdmission) (d authority.LeaseDecision, err error) {
	if p == nil {
		return authority.LeaseDecision{Kind: authority.LeaseAllow}, nil
	}
	defer func() {
		if r := recover(); r != nil {
			d, err = authority.LeaseDecision{}, fmt.Errorf("authoritycoord: concurrency provider panic: %v", r)
		}
	}()
	d, err = p.AdmitLease(ctx, in)
	if err != nil {
		return d, err
	}
	if vErr := d.Validate(); vErr != nil {
		// Keep d so callers can reverse-compensate any claimed leases (req 15.9).
		return d, vErr
	}
	return d, nil
}

func invokeCompensate(fn Compensator, ctx context.Context) (err error) {
	if fn == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("authoritycoord: compensate panic: %v", r)
		}
	}()
	return fn(ctx)
}

func isolateProviderPanic(d *authority.Decision, err *error) {
	if r := recover(); r != nil {
		*d = authority.Decision{}
		*err = fmt.Errorf("authoritycoord: provider panic: %v", r)
	}
}
