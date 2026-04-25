package extensions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
)

// RunRouteHintStage invokes route hint providers with fail-open semantics (design §17 route_hinting).
func RunRouteHintStage(ctx context.Context, log *slog.Logger, providers []routehint.Provider, call *lipapi.Call, meta routehint.Input) ([]string, error) {
	if call == nil {
		return nil, fmt.Errorf("extensions: nil call: %w", lipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return nil, fmt.Errorf("extensions: %w", lipapi.ErrNilContext)
	}
	var acc []string
	seen := map[string]struct{}{}
	sorted := routehint.MaterializeSorted(providers)
	for _, p := range sorted {
		if p == nil {
			continue
		}
		if execctx.IsSuppressedPluginID(ctx, p.ID()) {
			continue
		}
		res, err := safety.CallValue(safety.BoundaryExtension, "route_hint_provider", func() (routehint.Result, error) {
			return p.Hint(ctx, meta)
		})
		if err != nil {
			mode := p.FailureMode()
			if mode == sdkhooks.FailureModeUnspecified {
				mode = sdkhooks.FailOpen
			}
			if mode == sdkhooks.FailOpen {
				if log != nil {
					var pe *safety.PanicError
					if errors.As(err, &pe) {
						logFailOpenExtensionPanic(ctx, log, "route_hint", p.ID(), err)
					} else {
						log.WarnContext(ctx, "route_hinting: provider error (fail-open)", "provider", p.ID(), "error", err)
					}
				}
				continue
			}
			return nil, fmt.Errorf("route hint provider %q: %w", p.ID(), err)
		}
		for _, k := range res.PreferredCandidateKeys {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			acc = append(acc, k)
		}
	}
	return acc, nil
}
