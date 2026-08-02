package openresponses

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	corecont "github.com/matdev83/go-llm-interactive-proxy/internal/core/continuation"
	proto "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/protocols/openresponses"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

// RouteClaims calculates the normalized route claims for an OpenResponses frontend config.
func RouteClaims(cfg Config) ([]httpcontract.RouteClaim, error) {
	return RouteClaimsForOwner(cfg, ID)
}

// RouteClaimsForOwner returns claims with an explicit immutable owner identity.
func RouteClaimsForOwner(cfg Config, ownerID string) ([]httpcontract.RouteClaim, error) {
	if strings.ContainsAny(ownerID, "\r\n\t") {
		return nil, fmt.Errorf("openresponses: owner identity contains control characters")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("openresponses: invalid config: %w", err)
	}
	claims, err := httpcontract.OpenResponsesDefaultClaims(ownerID)
	if err != nil {
		return nil, err
	}

	if cfg.BasePath != httpcontract.DefaultOpenResponsesBasePath {
		remapped, err := httpcontract.RemapBasePath(claims, httpcontract.DefaultOpenResponsesBasePath, cfg.BasePath)
		if err != nil {
			return nil, fmt.Errorf("openresponses: failed to remap base_path: %w", err)
		}
		claims = remapped
	}

	if !cfg.WebSocket.IsEnabled() {
		filtered := make([]httpcontract.RouteClaim, 0, len(claims))
		for _, c := range claims {
			if c.Kind != httpcontract.RouteKindOpenResponsesWebSocket {
				filtered = append(filtered, c)
			}
		}
		claims = filtered
	}

	return claims, nil
}

// RegisterClaims validates and atomically adds this frontend's claims to a registry.
// No handler is mounted by this operation.
func RegisterClaims(reg *httpcontract.RouteRegistry, cfg Config) ([]httpcontract.RouteClaim, error) {
	if reg == nil {
		return nil, fmt.Errorf("openresponses: nil route registry")
	}
	claims, err := RouteClaims(cfg)
	if err != nil {
		return nil, err
	}
	// Validate against a copy first so a conflict cannot leave a partial registration.
	staged := httpcontract.NewRouteRegistry()
	if err := staged.RegisterAll(reg.Claims()); err != nil {
		return nil, err
	}
	if err := staged.RegisterAll(claims); err != nil {
		return nil, err
	}
	if err := reg.RegisterAll(claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// RegisterClaimsForOwner is the composition-root form when a frontend instance
// has an operator-assigned owner ID.
func RegisterClaimsForOwner(reg *httpcontract.RouteRegistry, cfg Config, ownerID string) ([]httpcontract.RouteClaim, error) {
	if reg == nil {
		return nil, fmt.Errorf("openresponses: nil route registry")
	}
	claims, err := RouteClaimsForOwner(cfg, ownerID)
	if err != nil {
		return nil, err
	}
	staged := httpcontract.NewRouteRegistry()
	if err := staged.RegisterAll(reg.Claims()); err != nil {
		return nil, err
	}
	if err := staged.RegisterAll(claims); err != nil {
		return nil, err
	}
	if err := reg.RegisterAll(claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// RouteDiagnostics returns stable, sanitized diagnostics for this validated config.
func RouteDiagnostics(cfg Config) []httpcontract.RouteDiagnostic {
	claims, err := RouteClaims(cfg)
	if err != nil {
		return nil
	}
	reg := httpcontract.NewRouteRegistry()
	if err := reg.RegisterAll(claims); err != nil {
		return nil
	}
	return httpcontract.RouteDiagnosticsFromRegistry(reg)
}

// Mount registers the OpenResponses API handler and route claims on mux.
func Mount(mux *http.ServeMux, opts lipsdk.FrontendMountOptions) error {
	cfg, err := DecodeConfig(opts.PluginCfg)
	if err != nil {
		return err
	}

	claims, err := RouteClaims(cfg)
	if err != nil {
		return err
	}

	// The standard runtime constructs one bounded continuation store per config
	// instance (no globals) and closes it exactly once when the generation begins
	// shutdown, so reload and reverse shutdown never leave an orphan store.
	store, bounds := newConfigContinuationStore(cfg)
	handler := NewHandler(HandlerConfig{
		Executor:             opts.Exec,
		DefaultRouteSelector: opts.DefaultRoute,
		RoutePrefixes:        opts.RoutePrefixes,
		MaxRequestBodyBytes:  opts.MaxRequestBodyBytes,
		ProtocolLimits:       proto.DefaultLimits(),
		DecodeAdmission:      opts.DecodeAdmission,
		TrafficPorts:         opts.TrafficPorts,
		PreRequestKeepalive:  opts.PreRequestKeepalive,
		ContinuationStore:    store,
		ContinuationResolver: NewStoreContinuationResolver(store, bounds),
		Config:               cfg,
	})
	if opts.GenerationContext != nil {
		_ = context.AfterFunc(opts.GenerationContext, func() { _ = store.Close() })
	}

	if mux != nil {
		createPath := cfg.BasePath + "/responses"
		compactPath := cfg.BasePath + "/responses/compact"

		mux.Handle("POST "+createPath, handler)
		mux.Handle("POST "+compactPath, handler)
		if cfg.WebSocket.IsEnabled() {
			wsPath := cfg.BasePath + "/responses"
			wsHandler := NewWebSocketHandler(WebSocketHandlerConfig{
				Config: cfg,
				Runner: NewSessionRunner(SessionRunnerConfig{
					Executor:             opts.Exec,
					DefaultRouteSelector: opts.DefaultRoute,
					RoutePrefixes:        opts.RoutePrefixes,
					MaxMessageBytes:      opts.MaxRequestBodyBytes,
					ProtocolLimits:       proto.DefaultLimits(),
				}),
				// The generation context is the runtime-owned shutdown signal:
				// every session of this handler observes it and closes exactly once
				// when the generation quiesces during reload or server shutdown.
				ShutdownCtx:     opts.GenerationContext,
				MaxMessageBytes: opts.MaxRequestBodyBytes,
			})
			mux.Handle("GET "+wsPath, wsHandler)
		}
	}

	_ = claims
	return nil
}

// newConfigContinuationStore derives a bounded in-memory continuation store and
// materialization bounds from a validated frontend config. Zero fields fall back
// to profile defaults so the default config stays consistent with the contract.
func newConfigContinuationStore(cfg Config) (*corecont.MemoryStore, lipcont.Bounds) {
	depth := cfg.Continuation.MaxChainDepth
	if depth <= 0 {
		depth = DefaultMaxChainDepth
	}
	maxBytes := cfg.Continuation.MaxMaterializedBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxMaterializedBytes
	}
	store := corecont.NewMemoryStoreWithLimits(lipcont.StorageLimits{
		MaxRecords:     10_000,
		MaxBytes:       maxBytes,
		MaxRecordBytes: 16 << 20,
		MaxChainDepth:  depth,
	})
	bounds := lipcont.Bounds{
		MaxChainDepth:        depth,
		MaxMaterializedBytes: maxBytes,
		MaxMaterializedItems: 100_000,
	}
	return store, bounds
}
