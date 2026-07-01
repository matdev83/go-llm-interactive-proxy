package usage

import (
	"context"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// Event is a provider-neutral usage observation emitted from canonical usage deltas.
type Event struct {
	TraceID     string
	ALegID      string
	BLegID      string
	PrincipalID string
	SessionID   string
	AttemptSeq  int
	BackendID   string
	FrontendID  string
	Model       string

	// Scope is the optional safe principal/scope attribution snapshot for this request. It is
	// additive metadata; existing observers may ignore it and keep reading PrincipalID
	// (requirements 6.4, 6.5, 7.3). When Scope.PrincipalID is known it must match PrincipalID.
	Scope scope.PrincipalScopeView

	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	ReasoningTokens  int
	TotalTokens      int

	CostNanoUnits int64
	Currency      string
	CostSource    string
	RawUsageJSON  string

	RecordedAt time.Time
}

// Observer records usage without mutating request or stream execution.
type Observer interface {
	OnUsage(ctx context.Context, ev Event) error
}

// NoopObserver drops usage events.
type NoopObserver struct{}

func (NoopObserver) OnUsage(context.Context, Event) error { return nil }

// ChainObserver fans usage events out to observers in registration order.
type ChainObserver struct {
	observers []Observer
}

func (c ChainObserver) OnUsage(ctx context.Context, ev Event) error {
	for _, o := range c.observers {
		if err := o.OnUsage(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}

var (
	_ Observer = NoopObserver{}
	_ Observer = ChainObserver{}
)

// ChainObservers returns a [ChainObserver] over non-nil observers (registration order preserved).
// An empty chain is a no-op observer.
func ChainObservers(observers ...Observer) ChainObserver {
	filtered := make([]Observer, 0, len(observers))
	for _, o := range observers {
		if o != nil {
			filtered = append(filtered, o)
		}
	}
	return ChainObserver{observers: filtered}
}
