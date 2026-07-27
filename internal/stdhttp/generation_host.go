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
)

// GenerationServeHost is the focused host-facing seam this adapter needs
// (req 8.6-8.7). It exposes the stable data-plane handler, early reload-trigger
// rejection, and the canonical process shutdown coordinator — never the
// generation manager, process runtime, or tracing primitives behind them.
type GenerationServeHost interface {
	// HTTPHandler returns the process-stable request dispatcher.
	HTTPHandler() http.Handler
	// BeginShutdown rejects new reload triggers before the HTTP drain starts.
	BeginShutdown()
	// Close runs the host-owned shutdown ordering after the HTTP drain.
	Close(context.Context) error
}

// GenerationHostInput configures [RunWithGenerationHost].
type GenerationHostInput struct {
	Config *config.Config
	Log    *slog.Logger
	// Host owns every generation/process/tracing shutdown decision.
	Host GenerationServeHost
	// Management is the optional process-owned management listener, closed in
	// its established position after the data plane drains and before the
	// host-owned teardown (task 5.6).
	Management interface {
		Shutdown(context.Context) error
	}
	// ShutdownTimeout bounds the HTTP drain and the host close. Zero uses 15s.
	ShutdownTimeout time.Duration
}

// httpServerShutdown is the [http.Server.Shutdown] implementation (overridable in tests).
var httpServerShutdown = func(ctx context.Context, srv *http.Server) error {
	return srv.Shutdown(ctx)
}

// RunWithGenerationHost serves one long-lived http.Server backed by the
// host-provided stable generation dispatcher. Startup listener/timeouts are
// fixed from Config. This adapter owns only the http.Server; shutdown is:
//
//  1. reject reload triggers (host.BeginShutdown)
//  2. stop and drain HTTP, then await the serve worker
//  3. close the optional management listener
//  4. invoke host.Close exactly once for that shutdown attempt
//
// If the HTTP drain fails or times out, host.Close is not invoked so live
// handlers are never closed underneath. Management and host errors are joined
// with any serve error and returned truthfully.
func RunWithGenerationHost(ctx context.Context, in GenerationHostInput) error {
	if ctx == nil {
		return errors.New("stdhttp: nil context")
	}
	if in.Host == nil {
		return errors.New("stdhttp: nil generation serve host")
	}
	timeout := in.ShutdownTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	handler, err := validateGenerationHostInput(in)
	if err != nil {
		return errors.Join(err, cleanupIncompleteServeHost(ctx, timeout, in))
	}

	srv := &http.Server{
		Addr:              in.Config.Server.Address,
		Handler:           handler,
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

	select {
	case <-ctx.Done():
		in.Host.BeginShutdown()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := httpServerShutdown(shutdownCtx, srv); err != nil {
			return fmt.Errorf("stdhttp: http server shutdown: %w", err)
		}
		// Successful Shutdown closes listeners and drains accepted connections.
		// Confirm the serving worker has exited before host teardown, and
		// preserve a concurrent listener failure if cancellation won select.
		var serveErr error
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				serveErr = fmt.Errorf("stdhttp: serve: %w", err)
			}
		case <-shutdownCtx.Done():
			return fmt.Errorf("stdhttp: await server exit: %w", shutdownCtx.Err())
		}
		return errors.Join(serveErr, closeServeHost(shutdownCtx, in))
	case err := <-errCh:
		in.Host.BeginShutdown()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		var out error
		if shutErr := httpServerShutdown(shutdownCtx, srv); shutErr != nil {
			// Accepted connections may still be live; do not let the host close
			// generations or process services underneath them.
			out = errors.Join(out, fmt.Errorf("stdhttp: http server shutdown: %w", shutErr))
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				out = errors.Join(out, fmt.Errorf("stdhttp: serve: %w", err))
			}
			return out
		}
		out = errors.Join(out, closeServeHost(shutdownCtx, in))
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return out
		}
		return errors.Join(out, fmt.Errorf("stdhttp: serve: %w", err))
	}
}

// validateGenerationHostInput checks config/log/security/handler after a non-nil
// Host is already present and returns the resolved handler so the serve path
// mounts exactly the validated value (HTTPHandler is never re-read). Failures
// must converge on the cleanup seam so process/tracing ownership is never
// stranded.
func validateGenerationHostInput(in GenerationHostInput) (http.Handler, error) {
	if in.Config == nil {
		return nil, errors.New("stdhttp: nil config")
	}
	if in.Log == nil {
		return nil, errors.New("stdhttp: nil logger")
	}
	if err := validateStartupSecurity(in.Config); err != nil {
		return nil, fmt.Errorf("stdhttp: validate startup security: %w", err)
	}
	handler := in.Host.HTTPHandler()
	if handler == nil {
		return nil, errors.New("stdhttp: nil generation handler")
	}
	return handler, nil
}

// cleanupIncompleteServeHost is the sole post-Host input-failure cleanup path:
// BeginShutdown, optional management close, then Host.Close once.
func cleanupIncompleteServeHost(ctx context.Context, timeout time.Duration, in GenerationHostInput) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	in.Host.BeginShutdown()
	return closeServeHost(cleanupCtx, in)
}

// closeServeHost closes the optional management listener and then invokes the
// canonical host close exactly once. A management failure is joined but never
// skips the host close.
func closeServeHost(ctx context.Context, in GenerationHostInput) error {
	var out error
	if in.Management != nil {
		if err := in.Management.Shutdown(ctx); err != nil {
			out = errors.Join(out, err)
		}
	}
	if in.Host != nil {
		if err := in.Host.Close(ctx); err != nil {
			out = errors.Join(out, err)
		}
	}
	return out
}
