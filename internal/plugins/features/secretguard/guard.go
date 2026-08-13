package secretguard

import (
	"context"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

type guard struct {
	cfg Config
}

// NewGuard returns a Guard for the given validated config.
func NewGuard(cfg Config) sdk.Guard {
	return &guard{cfg: cfg}
}

func (g *guard) ID() string { return ID }

func (g *guard) Order() int {
	if g.cfg.Order != nil {
		return *g.cfg.Order
	}
	return 0
}

func (g *guard) FailureMode() sdk.FailureMode {
	return sdk.FailClosed
}

func (g *guard) Evaluate(ctx context.Context, call *lipapi.Call, _ sdk.Meta, services sdk.Services) (sdk.Decision, error) {
	if call == nil {
		return sdk.Decision{Outcome: sdk.OutcomePass}, nil
	}
	if services.MatcherResolver == nil {
		return sdk.Decision{Outcome: sdk.OutcomePass}, nil
	}
	matcher, err := services.MatcherResolver.Resolve(ctx)
	if err != nil {
		return sdk.Decision{}, err
	}
	if matcher == nil {
		return sdk.Decision{Outcome: sdk.OutcomePass}, nil
	}

	switch g.cfg.Action {
	case ActionBlock:
		return g.evalBlock(ctx, call, matcher)
	case ActionRedact:
		return g.evalRedact(ctx, call, matcher)
	case ActionLog:
		return g.evalLog(ctx, call, matcher)
	default:
		return sdk.Decision{}, fmt.Errorf("%s: unknown action %q", ID, g.cfg.Action)
	}
}

func (g *guard) evalBlock(ctx context.Context, call *lipapi.Call, m sdk.Matcher) (sdk.Decision, error) {
	out, err := scanCall(ctx, call, m, modeScan, g.cfg.ScanMaxBytes)
	if err != nil {
		return sdk.Decision{}, err
	}
	d := sdk.Decision{Findings: out.Findings}
	if out.ScanLimitHit {
		return scanLimitDecision(d, sdk.OutcomeBlock), nil
	}
	if len(out.Findings) > 0 {
		d.Outcome = sdk.OutcomeBlock
		return d, nil
	}
	d.Outcome = sdk.OutcomePass
	return d, nil
}

func (g *guard) evalRedact(ctx context.Context, call *lipapi.Call, m sdk.Matcher) (sdk.Decision, error) {
	// One-pass canonical location traversal: redact on a working clone so findings,
	// mutations, and scan-limit accounting share a single matcher walk. The live
	// call is only replaced when MutationCount > 0 (no partial commit on failure/limit).
	work := lipapi.CloneCall(*call)
	out, err := scanCall(ctx, &work, m, modeRedact, g.cfg.ScanMaxBytes)
	if err != nil {
		var unsupported *unsupportedJSONTokenError
		if errors.As(err, &unsupported) {
			return sdk.Decision{
				Outcome:       sdk.OutcomeBlock,
				Findings:      out.Findings,
				MutationCount: 0,
				FailureKind:   FailureKindUnsupportedJSONToken,
				FailureReason: "unsupported JSON token encountered",
			}, nil
		}
		return sdk.Decision{}, err
	}
	d := sdk.Decision{
		Findings:      out.Findings,
		MutationCount: out.MutationCount,
	}
	if out.ScanLimitHit {
		return scanLimitDecision(d, sdk.OutcomeBlock), nil
	}
	if out.MutationCount > 0 {
		*call = work
		d.Outcome = sdk.OutcomeRedacted
		return d, nil
	}
	if len(out.Findings) > 0 {
		d.Outcome = sdk.OutcomeLog
		return d, nil
	}
	d.Outcome = sdk.OutcomePass
	return d, nil
}

func (g *guard) evalLog(ctx context.Context, call *lipapi.Call, m sdk.Matcher) (sdk.Decision, error) {
	out, err := scanCall(ctx, call, m, modeScan, g.cfg.ScanMaxBytes)
	if err != nil {
		return sdk.Decision{}, err
	}
	d := sdk.Decision{Findings: out.Findings}
	if out.ScanLimitHit {
		return scanLimitDecision(d, sdk.OutcomeLog), nil
	}
	if len(out.Findings) > 0 {
		d.Outcome = sdk.OutcomeLog
		return d, nil
	}
	d.Outcome = sdk.OutcomePass
	return d, nil
}

func scanLimitDecision(base sdk.Decision, outcome sdk.Outcome) sdk.Decision {
	base.ScanLimitHit = true
	base.MutationCount = 0
	base.FailureKind = FailureKindScanLimit
	base.FailureReason = "scan_max_bytes exceeded"
	base.Outcome = outcome
	return base
}
