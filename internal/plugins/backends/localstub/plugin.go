package localstub

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

// ID is the registered backend factory kind for the standard distribution.
const ID = "local-stub"

// NewFromYAML builds a backend from opaque plugin YAML (composition root).
func NewFromYAML(n yaml.Node) (execbackend.Backend, error) {
	cfg, err := ParseConfig(n)
	if err != nil {
		return execbackend.Backend{}, fmt.Errorf("local-stub: from yaml: %w", err)
	}
	return New(cfg), nil
}

// New returns an exec backend that emits a fixed canonical stream from cfg.
func New(cfg Config) execbackend.Backend {
	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming)
	if cfg.ToolName != "" {
		caps[lipapi.CapabilityTools] = struct{}{}
	}
	return execbackend.Backend{
		Caps: caps,
		ResolveCaps: func(context.Context, lipapi.Call, routing.AttemptCandidate) lipapi.BackendCaps {
			return caps
		},
		ModelInventory: modelinventory.StaticProvider{
			Source: modelinventory.SourceStaticBuiltin,
			Models: []modelinventory.Model{{
				CanonicalID: "local-stub/stub-default",
				NativeID:    "stub-default",
				DisplayName: "Local Stub Default",
			}},
		},
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", ID, lipapi.ErrNilContext)
			}
			_ = call
			_ = cand
			return eventStreamForConfig(cfg), nil
		},
	}
}
