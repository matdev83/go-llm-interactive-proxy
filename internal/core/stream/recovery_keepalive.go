package stream

import (
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// DefaultRecoveryKeepaliveInterval is used while waiting on upstream streams or
// executor recv-phase failover (Req 5.5).
const DefaultRecoveryKeepaliveInterval = 12 * time.Second

// WrapRecoveryKeepalive wraps the canonical event stream so idle reads emit
// protocol-neutral Warning keepalives (see DefaultKeepaliveEvent).
func WrapRecoveryKeepalive(s lipapi.EventStream) (lipapi.EventStream, error) {
	return NewKeepalive(s, KeepaliveConfig{
		Interval:     DefaultRecoveryKeepaliveInterval,
		NewKeepalive: DefaultKeepaliveEvent,
	})
}
