package stdhttp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)

// GenerationHostInput configures [RunWithGenerationHost].
type GenerationHostInput struct {
	Config  *config.Config
	Log     *slog.Logger
	Manager *runtimehost.Manager
	Process *runtimebundle.ProcessServices
	// ShutdownTimeout bounds HTTP drain and generation retirement waits.
	// Zero uses 15s.
	ShutdownTimeout time.Duration
}

// httpServerShutdown is the [http.Server.Shutdown] implementation (overridable in tests).
var httpServerShutdown = func(ctx context.Context, srv *http.Server) error {
	return srv.Shutdown(ctx)
}

// RunWithGenerationHost serves one long-lived http.Server backed by a stable
// generation dispatcher. Startup listener/timeouts are fixed from Config.
// Shutdown order: stop HTTP → detach/retire generations → close ProcessServices.
// Tracing remains owned by the outer bootstrap defer (req 5.1, 13.x, 16.3).
//
// If HTTP drain fails/times out, generation and process resources are left open
// so live handlers are not closed underneath. Generation/process shutdown errors
// are joined and returned to the caller.
func RunWithGenerationHost(ctx context.Context, in GenerationHostInput) error {
	if ctx == nil {
		return errors.New("stdhttp: nil context")
	}
	if in.Config == nil {
		return errors.New("stdhttp: nil config")
	}
	if in.Log == nil {
		return errors.New("stdhttp: nil logger")
	}
	if in.Manager == nil {
		return errors.New("stdhttp: nil generation manager")
	}
	if in.Process == nil {
		return errors.New("stdhttp: nil process services")
	}
	if err := validateStartupSecurity(in.Config); err != nil {
		cleanupErr := shutdownGenerationHost(context.WithoutCancel(ctx), in, 0)
		return errors.Join(fmt.Errorf("stdhttp: validate startup security: %w", err), cleanupErr)
	}

	dispatcher := runtimehost.NewGenerationDispatcher(in.Manager)
	srv := &http.Server{
		Addr:              in.Config.Server.Address,
		Handler:           dispatcher,
		ReadHeaderTimeout: in.Config.Server.EffectiveReadHeaderTimeout(),
		ReadTimeout:       in.Config.Server.EffectiveReadTimeout(),
		WriteTimeout:      in.Config.Server.EffectiveWriteTimeout(),
		IdleTimeout:       in.Config.Server.EffectiveIdleTimeout(),
	}
	in.Log.InfoContext(ctx, "listening", "addr", in.Config.Server.Address)

	errCh := make(chan error, 1)
	go func() {
		err := func() (err error) {
			defer func() {
				if p := recover(); p != nil {
					pe := safety.Capture(safety.BoundaryWorker, "listen_and_serve", p)
					if in.Log != nil {
						logCtx := context.WithoutCancel(ctx)
						attrs := diag.IsolatedCrashAttrs(logCtx, pe, diag.CrashAttrOpts{})
						attrs = diag.AppendIsolatedCrashStack(attrs, pe)
						in.Log.LogAttrs(logCtx, slog.LevelError, "stdhttp: isolated panic in listenAndServe worker", attrs...)
					}
					err = pe
				}
			}()
			return listenAndServe(srv)
		}()
		errCh <- err
	}()

	timeout := in.ShutdownTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := httpServerShutdown(shutdownCtx, srv); err != nil {
			return fmt.Errorf("stdhttp: http server shutdown: %w", err)
		}
		// Successful Shutdown closes listeners and drains accepted connections.
		// Confirm the serving worker has exited before retiring its generation,
		// and preserve a concurrent listener failure if cancellation won select.
		var serveErr error
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				serveErr = fmt.Errorf("stdhttp: serve: %w", err)
			}
		case <-shutdownCtx.Done():
			return fmt.Errorf("stdhttp: await server exit: %w", shutdownCtx.Err())
		}
		genErr := shutdownGenerationHost(shutdownCtx, in, timeout)
		return errors.Join(serveErr, genErr)
	case err := <-errCh:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		var out error
		if shutErr := httpServerShutdown(shutdownCtx, srv); shutErr != nil {
			// Accepted connections may still be live; do not retire generations
			// or close process services underneath them.
			out = errors.Join(out, fmt.Errorf("stdhttp: http server shutdown: %w", shutErr))
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				out = errors.Join(out, fmt.Errorf("stdhttp: serve: %w", err))
			}
			return out
		}
		if genErr := shutdownGenerationHost(shutdownCtx, in, timeout); genErr != nil {
			out = errors.Join(out, genErr)
		}
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return out
		}
		return errors.Join(out, fmt.Errorf("stdhttp: serve: %w", err))
	}
}

func shutdownGenerationHost(ctx context.Context, in GenerationHostInput, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
		defer cancel()
	}
	var out error
	if in.Manager != nil {
		if err := in.Manager.ShutdownDetached(ctx, runtimehost.NewLifecycleWorker()); err != nil {
			out = errors.Join(out, err)
		}
	}
	// Never close ProcessServices while any generation remains open/pinned.
	if in.Process != nil && (in.Manager == nil || !in.Manager.HasOpenGenerations()) {
		if err := closeProcessServices(in.Process); err != nil {
			out = errors.Join(out, err)
		}
	}
	return out
}

// closeProcessServices closes process-scoped resources (overridable in tests).
var closeProcessServices = func(ps *runtimebundle.ProcessServices) error {
	if ps == nil {
		return nil
	}
	return ps.Close()
}
