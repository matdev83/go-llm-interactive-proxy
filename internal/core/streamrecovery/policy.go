package streamrecovery

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type Config struct {
	Enabled          bool
	IdleTimeout      time.Duration
	GracePeriod      time.Duration
	EmitWarning      bool
	PostOutputPolicy string
}

type DecisionKind string

const (
	DecisionPassThrough      DecisionKind = "pass_through"
	DecisionRecoverPreOutput DecisionKind = "recover_pre_output"
	DecisionFinishPostOutput DecisionKind = "finish_post_output"
	DecisionSurfaceFailure   DecisionKind = "surface_failure"
)

type Decision struct {
	Kind    DecisionKind
	Err     error
	Warning lipapi.Event
	Finish  lipapi.Event
	Reason  string
}

type Policy struct {
	cfg              Config
	lastActivityAt   time.Time
	clientCommitted  bool
	responseFinished bool
}

func NewPolicy(cfg Config, start time.Time) *Policy {
	return &Policy{cfg: cfg, lastActivityAt: start}
}

func (p *Policy) ObserveBackendEvent(ev lipapi.Event, at time.Time) {
	if p == nil {
		return
	}
	p.lastActivityAt = at
	if ev.Kind == lipapi.EventResponseFinished {
		p.responseFinished = true
	}
}

func (p *Policy) ObserveClientEvent(ev lipapi.Event, at time.Time) {
	if p == nil {
		return
	}
	p.lastActivityAt = at
	if lipapi.OutputCommitted(ev) {
		p.clientCommitted = true
	}
	if ev.Kind == lipapi.EventResponseFinished {
		p.responseFinished = true
	}
}

func (p *Policy) DecideEOF(err error, now time.Time) Decision {
	if p == nil || !p.cfg.Enabled || err == nil || p.responseFinished {
		return Decision{Kind: DecisionPassThrough, Err: err}
	}
	if !errors.Is(err, io.EOF) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Decision{Kind: DecisionSurfaceFailure, Err: err}
		}
		if p.clientCommitted {
			return p.finishPostOutput("upstream error after client output")
		}
		return Decision{Kind: DecisionRecoverPreOutput, Err: lipapi.RecoverablePreOutputError(err), Reason: "upstream error before response_finished"}
	}
	if p.clientCommitted {
		return p.finishPostOutput("upstream EOF before response_finished")
	}
	_ = now
	return Decision{Kind: DecisionRecoverPreOutput, Err: lipapi.RecoverablePreOutputError(io.EOF), Reason: "upstream EOF before response_finished"}
}

func (p *Policy) DecideIdle(now time.Time) Decision {
	if p == nil || !p.cfg.Enabled || p.responseFinished {
		return Decision{Kind: DecisionPassThrough}
	}
	deadline, ok := p.IdleDeadline()
	if !ok || now.Before(deadline) {
		return Decision{Kind: DecisionPassThrough}
	}
	if p.clientCommitted {
		return p.finishPostOutput("upstream idle timeout after client output")
	}
	return Decision{Kind: DecisionRecoverPreOutput, Err: lipapi.RecoverablePreOutputError(context.DeadlineExceeded), Reason: "upstream idle timeout before response_finished"}
}

func (p *Policy) IdleDeadline() (time.Time, bool) {
	if p == nil || !p.cfg.Enabled || p.responseFinished {
		return time.Time{}, false
	}
	limit := p.cfg.IdleTimeout + p.cfg.GracePeriod
	if limit <= 0 || p.lastActivityAt.IsZero() {
		return time.Time{}, false
	}
	return p.lastActivityAt.Add(limit), true
}

func (p *Policy) finishPostOutput(reason string) Decision {
	dec := Decision{
		Kind:   DecisionFinishPostOutput,
		Reason: reason,
		Finish: lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "proxy_stream_recovered"},
	}
	if p.cfg.EmitWarning {
		dec.Warning = lipapi.Event{
			Kind:           lipapi.EventWarning,
			WarningCode:    "proxy_stream_recovery",
			WarningMessage: reason,
		}
	}
	return dec
}
