// Package preflight evaluates token-accounting admission checks before a backend attempt.
package preflight

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type Mode string

const (
	ModeAdvisory Mode = "advisory"
	ModeStrict   Mode = "strict"
)

type Reason string

const (
	ReasonAllowed              Reason = "allowed"
	ReasonDisabled             Reason = "disabled"
	ReasonCountUnavailable     Reason = "count_unavailable"
	ReasonInputLimitExceeded   Reason = "input_limit_exceeded"
	ReasonContextLimitExceeded Reason = "context_limit_exceeded"
	ReasonOutputLimitExceeded  Reason = "output_limit_exceeded"
)

type Counter interface {
	CountCall(context.Context, app.CountCallInput) (app.CountResult, error)
}

type Config struct {
	Enabled              bool
	Mode                 Mode
	MaxInputTokens       int64
	MaxOutputTokens      int64
	MaxContextTokens     int64
	ClampMaxOutputTokens bool
}

type Checker struct {
	counter Counter
	cfg     Config
}

type Input struct {
	Backend                  string
	Model                    string
	CallID                   string
	Call                     lipapi.Call
	RequestedMaxOutputTokens *int
	Facts                    modelcatalog.ModelFacts
}

type Decision struct {
	Allowed                 bool
	Reason                  Reason
	Warnings                []string
	Err                     error
	Count                   app.CountResult
	AdjustedMaxOutputTokens *int
}

func NewChecker(counter Counter, cfg Config) *Checker {
	if cfg.Mode == "" {
		cfg.Mode = ModeAdvisory
	}
	return &Checker{counter: counter, cfg: cfg}
}

func (c *Checker) Check(ctx context.Context, in Input) Decision {
	if c == nil || !c.cfg.Enabled {
		return Decision{Allowed: true, Reason: ReasonDisabled}
	}
	if c.counter == nil {
		return c.failOrWarn(ReasonCountUnavailable, "token count unavailable: nil counter", nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	count, err := c.counter.CountCall(ctx, app.CountCallInput{
		Backend: in.Backend,
		Model:   in.Model,
		CallID:  in.CallID,
		Call:    in.Call,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Decision{Allowed: false, Reason: ReasonCountUnavailable, Err: err}
		}
		return c.failOrWarn(ReasonCountUnavailable, fmt.Sprintf("token count unavailable: %v", err), err)
	}
	if count.InputTokens < 0 {
		return c.failOrWarn(ReasonCountUnavailable, fmt.Sprintf("token count invalid: input tokens %d", count.InputTokens), nil)
	}

	out := Decision{Allowed: true, Reason: ReasonAllowed, Count: count}
	if c.cfg.MaxInputTokens > 0 && int64(count.InputTokens) > c.cfg.MaxInputTokens {
		return Decision{Allowed: false, Reason: ReasonInputLimitExceeded, Count: count}
	}

	outputLimit := effectiveLimitFact(in.Facts.OutputLimit, c.cfg.MaxOutputTokens)
	effectiveOutput, adjusted, ok := c.evaluateOutputLimit(in.RequestedMaxOutputTokens, outputLimit, count)
	if !ok {
		return adjusted
	}
	if adjusted.AdjustedMaxOutputTokens != nil {
		out.AdjustedMaxOutputTokens = adjusted.AdjustedMaxOutputTokens
	}
	if len(adjusted.Warnings) > 0 {
		out.Warnings = append(out.Warnings, adjusted.Warnings...)
	}

	contextLimit := effectiveLimitFact(in.Facts.ContextLimit, c.cfg.MaxContextTokens)
	if limitPresent(contextLimit) && exceedsLimit(int64(count.InputTokens), effectiveOutput, contextLimit.Tokens) {
		return Decision{Allowed: false, Reason: ReasonContextLimitExceeded, Count: count}
	}
	return out
}

func (c *Checker) evaluateOutputLimit(
	requested *int,
	limit modelcatalog.LimitFact,
	count app.CountResult,
) (int64, Decision, bool) {
	effectiveOutput := int64(requestedOutputTokens(requested))
	if requested == nil || !limitPresent(limit) {
		return effectiveOutput, Decision{}, true
	}

	if int64(*requested) > limit.Tokens {
		if c.cfg.ClampMaxOutputTokens {
			clamped := int(limit.Tokens)
			return int64(clamped), Decision{Allowed: true, Reason: ReasonAllowed, Count: count, AdjustedMaxOutputTokens: &clamped}, true
		}
		if c.cfg.Mode == ModeStrict {
			return 0, Decision{Allowed: false, Reason: ReasonOutputLimitExceeded, Count: count}, false
		}
		warning := fmt.Sprintf("requested max output tokens %d exceeds output limit %d", *requested, limit.Tokens)
		return effectiveOutput, Decision{Allowed: true, Reason: ReasonAllowed, Count: count, Warnings: []string{warning}}, true
	}

	return effectiveOutput, Decision{}, true
}

func (c *Checker) failOrWarn(reason Reason, warning string, err error) Decision {
	if c.cfg.Mode == ModeStrict {
		return Decision{Allowed: false, Reason: reason, Err: err}
	}
	return Decision{Allowed: true, Reason: ReasonAllowed, Warnings: []string{warning}, Err: err}
}

func requestedOutputTokens(tokens *int) int {
	if tokens == nil || *tokens < 0 {
		return 0
	}
	return *tokens
}

func limitPresent(limit modelcatalog.LimitFact) bool {
	return limit.State == modelcatalog.LimitPresent && limit.Tokens > 0
}

func effectiveLimitFact(fact modelcatalog.LimitFact, configured int64) modelcatalog.LimitFact {
	if configured <= 0 {
		return fact
	}
	if !limitPresent(fact) || configured < fact.Tokens {
		return modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: configured}
	}
	return fact
}

func exceedsLimit(input, output, limit int64) bool {
	if input > math.MaxInt64-output {
		return true
	}
	return input+output > limit
}
