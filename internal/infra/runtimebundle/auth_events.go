package runtimebundle

import (
	"fmt"
	"log/slog"
	"strings"

	coreauth "github.com/matdev83/go-llm-interactive-proxy/internal/core/auth"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/authevent"
)

func buildAuthEventDispatcher(cfg *config.Config, log *slog.Logger, opts *BuildOptions) (*coreauth.EventDispatcher, error) {
	if cfg == nil {
		return nil, fmt.Errorf("runtimebundle: nil config")
	}
	var injected coreauth.EventSink
	if opts != nil && opts.AuthEventSink != nil {
		injected = opts.AuthEventSink
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.Auth.EventDelivery))
	if mode == "" {
		mode = "default"
	}
	if injected != nil && mode != "custom" {
		return nil, fmt.Errorf("%w: use auth.event_delivery: custom to wire BuildOptions.AuthEventSink (got %q)", ErrAuthEventSinkDisallowed, cfg.Auth.EventDelivery)
	}
	var sink coreauth.EventSink
	switch mode {
	case "default":
		s, err := authevent.NewSlogEventSink(log)
		if err != nil {
			return nil, err
		}
		sink = s
	case "disabled":
		sink = nil
	case "custom":
		if injected == nil {
			return nil, fmt.Errorf("%w", ErrAuthEventSinkRequired)
		}
		sink = injected
	default:
		return nil, fmt.Errorf("runtimebundle: invalid auth.event_delivery %q", cfg.Auth.EventDelivery)
	}
	pol := coreauth.EventFailureBestEffort
	if strings.EqualFold(strings.TrimSpace(cfg.Auth.EventFailurePolicy), "fail_closed") {
		pol = coreauth.EventFailureFailClosed
	}
	return coreauth.NewEventDispatcher(sink, pol), nil
}
