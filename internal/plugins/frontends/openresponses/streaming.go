package openresponses

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	proto "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/protocols/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

const maxStreamingEvents = 100_000

type sseFlusher interface {
	Flush()
}

// sseResponseWriter is a response-writer seam for SSE streams. It sets the SSE
// headers exactly once, immediately before the first event bytes reach the
// underlying writer, and derives its committed state from the actual write
// result rather than from the success of the enclosing event. Once any bytes
// have been accepted (n > 0) or a write succeeded, the response is committed:
// callers must then only terminate/cleanup and must never attempt a JSON error
// response or header mutation. A failure with zero bytes accepted keeps the
// seam uncommitted so a normal JSON error is still possible.
type sseResponseWriter struct {
	w         http.ResponseWriter
	committed bool
}

func (s *sseResponseWriter) Header() http.Header {
	return s.w.Header()
}

func (s *sseResponseWriter) Write(p []byte) (int, error) {
	if !s.committed {
		h := s.w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")
		n, err := s.w.Write(p)
		if n > 0 || err == nil {
			s.committed = true
		}
		return n, err
	}
	return s.w.Write(p)
}

func (s *sseResponseWriter) Flush() {
	// net/http may commit the response when Flush is called even if no body
	// write reached the underlying writer. Keep the seam's state aligned with
	// that transport behavior so callers never attempt a JSON fallback after a
	// flush has committed headers.
	if !s.committed {
		s.committed = true
	}
	if f, ok := s.w.(sseFlusher); ok {
		f.Flush()
	}
}

func (h *Handler) serveStreaming(ctx context.Context, w http.ResponseWriter, stream lipapi.EventStream, decoded *DecodedCreate, responseID string, store lipcont.Store, scope lipcont.Scope, isReserved bool, owner continuationReservationOwner) {
	if stream == nil {
		if isReserved && store != nil {
			cleanupContinuationReservation(store, scope, responseID)
		}
		writeWireError(w, http.StatusBadGateway, "server_error", "backend_error", "Backend execution failed")
		return
	}
	var sm *proto.StateMachine
	terminalSuccess := false
	cleanupConsumed := false
	defer stream.Close()
	defer func() {
		if terminalSuccess || cleanupConsumed {
			return
		}
		if isReserved && store != nil {
			// Frontend-owned reservations are released on every non-terminal
			// exit, including validation/state-machine failures.
			cleanupContinuationReservation(store, scope, responseID)
			return
		}
		if owner != nil {
			// Recorder ownership was transferred, but no successful terminal
			// record exists. Release the reservation exactly once.
			safeReleaseContinuationReservation(owner)
		}
	}()

	if responseID == "" {
		ids := h.cfg.ResponseIDSource
		if ids == nil {
			ids = systemResponseIDSource{}
		}
		responseID = ids.NewResponseID()
	}
	clock := h.cfg.ResponseClock
	if clock == nil {
		clock = systemResponseClock{}
	}
	sm = proto.NewStateMachine(proto.EnvelopeMetadata{
		ResponseID:         responseID,
		PreviousResponseID: decoded.PreviousResponseID,
		CreatedAt:          clock.Now(),
		Model:              decoded.Model,
		Store:              &decoded.Store,
	}, decoded.Call.Options, effectiveProtocolLimits(h.cfg.ProtocolLimits))
	seam := &sseResponseWriter{w: w}
	sse := proto.NewSSEWriter(seam)
	committed := func() bool { return seam.committed }

	flush := func() {
		seam.Flush()
	}
	writeEvents := func(events []proto.StreamEvent) error {
		for _, event := range events {
			if err := sse.WriteEvent(event); err != nil {
				if !committed() {
					// No event bytes reached the wire. Drop the SSE headers so
					// the JSON error response is not polluted by them.
					w.Header().Del("Content-Type")
					w.Header().Del("Cache-Control")
					w.Header().Del("Connection")
				}
				return err
			}
			flush()
		}
		return nil
	}

	for eventCount := 0; eventCount < maxStreamingEvents; eventCount++ {
		ev, err := stream.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if !committed() {
					if isReserved && store != nil {
						cleanupContinuationReservation(store, scope, responseID)
						isReserved = false
					}
					writeWireError(w, http.StatusBadGateway, "server_error", "backend_error", "Backend execution failed")
				} else {
					_ = emitStreamFailure(sse, sm, flush)
				}
				return
			}
			if ctx.Err() != nil {
				if committed() && owner != nil {
					finalizeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
					finalizeErr := safeFinalizeIncomplete(owner, finalizeCtx)
					cancel()
					if finalizeErr == nil {
						terminalSuccess = true
					} else {
						// A finalizer may panic before consuming cleanup. Use the
						// explicit capability to decide whether a fallback release
						// is safe; never release after delete-then-error paths.
						if !safeCleanupConsumed(owner) {
							safeReleaseContinuationReservation(owner)
						}
						cleanupConsumed = true
					}
				}
				return
			}

			if committed() {
				_ = emitStreamFailure(sse, sm, flush)
			}
			if !committed() {
				if isReserved && store != nil {
					cleanupContinuationReservation(store, scope, responseID)
					isReserved = false
				}
				status, typ, code, message := classifyExecutionError(err)
				writeWireError(w, status, typ, code, message)
			}
			return
		}
		if err := lipapi.ValidateEventEnvelope(&ev); err != nil {
			if committed() {
				_ = emitStreamFailure(sse, sm, flush)
			} else {
				if isReserved && store != nil {
					cleanupContinuationReservation(store, scope, responseID)
					isReserved = false
				}
				writeWireError(w, http.StatusBadGateway, "server_error", "backend_error", "Backend execution failed")
			}
			return
		}
		// Preserve canonical provider semantics. Only fill absent fields when
		// the backend supplied an incomplete EventError.
		if ev.Kind == lipapi.EventError {
			if ev.ErrorCode == "" {
				ev.ErrorCode = "backend_error"
			}
			ev.ErrorMessage = safeCanonicalErrorMessage(ev.ErrorMessage)
		}
		events, processErr := sm.ProcessCanonicalEvent(ev)
		if processErr != nil {
			if committed() {
				_ = emitStreamFailure(sse, sm, flush)
			} else {
				if isReserved && store != nil {
					cleanupContinuationReservation(store, scope, responseID)
					isReserved = false
				}
				writeWireError(w, http.StatusBadGateway, "server_error", "backend_error", "Backend execution failed")
			}
			return
		}
		if err := writeEvents(events); err != nil {
			if !committed() {
				if isReserved && store != nil {
					cleanupContinuationReservation(store, scope, responseID)
					isReserved = false
				}
				writeWireError(w, http.StatusBadGateway, "server_error", "backend_error", "Backend execution failed")
			}
			return
		}
		if sm.State() == proto.StateTerminal {
			terminalSuccess = ev.Kind == lipapi.EventResponseFinished
			if err := sse.WriteDONE(); err == nil {
				flush()
			}
			return
		}
	}
	if committed() {
		_ = emitStreamFailure(sse, sm, flush)
	} else {
		// The loop can exhaust without a committed event when a backend streams
		// only no-output records (e.g. pure usage deltas) forever. Never return
		// silently: the client must receive a terminal error or DONE.
		if isReserved && store != nil {
			cleanupContinuationReservation(store, scope, responseID)
			isReserved = false
		}
		writeWireError(w, http.StatusBadGateway, "server_error", "backend_error", "Backend execution failed")
	}
}

func classifyCanonicalEventError(ev lipapi.Event) (int, string, string, string) {
	code := ev.ErrorCode
	if code == "" {
		code = "backend_error"
	}
	message := safeCanonicalErrorMessage(ev.ErrorMessage)
	switch code {
	case "invalid_request", "invalid_request_error", "context_length_exceeded":
		return http.StatusBadRequest, "invalid_request_error", code, message
	case "rate_limit_exceeded", "rate_limit_error":
		return http.StatusTooManyRequests, "rate_limit_error", code, message
	case "authentication_error":
		return http.StatusUnauthorized, "authentication_error", code, message
	case "permission_error":
		return http.StatusForbidden, "permission_error", code, message
	default:
		return http.StatusBadGateway, "server_error", code, message
	}
}

func emitStreamFailure(sse *proto.SSEWriter, sm *proto.StateMachine, flush func()) (err error) {
	doneAttempted := false
	defer func() {
		if doneAttempted {
			return
		}
		doneAttempted = true
		doneErr := sse.WriteDONE()
		if err == nil {
			err = doneErr
		}
		if doneErr == nil {
			flush()
		}
	}()

	if sm.State() == proto.StateTerminal {
		doneAttempted = true
		err = sse.WriteDONE()
		if err == nil {
			flush()
		}
		return err
	}
	events, err := sm.ProcessCanonicalEvent(lipapi.Event{
		Kind:         lipapi.EventError,
		ErrorCode:    "backend_error",
		ErrorMessage: "Backend execution failed",
	})
	if err != nil {
		return err
	}
	for _, event := range events {
		if err := sse.WriteEvent(event); err != nil {
			return err
		}
		flush()
	}
	return nil
}
