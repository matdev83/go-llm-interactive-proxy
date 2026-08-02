package openresponses

import (
	"context"
	"errors"
	"io"
	"net/http"

	proto "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/protocols/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

const maxStreamingEvents = 100_000

type sseFlusher interface {
	Flush()
}

func (h *Handler) serveStreaming(ctx context.Context, w http.ResponseWriter, stream lipapi.EventStream, decoded *DecodedCreate, responseID string, store lipcont.Store, scope lipcont.Scope, isReserved bool) {
	if stream == nil {
		if isReserved && store != nil {
			cleanupContinuationReservation(store, scope, responseID)
		}
		writeWireError(w, http.StatusBadGateway, "server_error", "backend_error", "Backend execution failed")
		return
	}
	var sm *proto.StateMachine
	terminalSuccess := false
	defer stream.Close()
	defer func() {
		if isReserved && store != nil && !terminalSuccess {
			cleanupContinuationReservation(store, scope, responseID)
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
	sse := proto.NewSSEWriter(w)
	committed := false
	streamHeadersWritten := false

	flush := func() {
		if f, ok := w.(sseFlusher); ok {
			f.Flush()
		}
	}
	writeEvents := func(events []proto.StreamEvent) error {
		for _, event := range events {
			if !streamHeadersWritten {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
			}
			if err := sse.WriteEvent(event); err != nil {
				if !committed {
					w.Header().Del("Content-Type")
					w.Header().Del("Cache-Control")
					w.Header().Del("Connection")
				}
				return err
			}
			streamHeadersWritten = true
			committed = true
			flush()
		}
		return nil
	}

	for eventCount := 0; eventCount < maxStreamingEvents; eventCount++ {
		ev, err := stream.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if !committed {
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
				if !committed && isReserved && store != nil {
					cleanupContinuationReservation(store, scope, responseID)
					isReserved = false
				}
				return
			}
			if committed {
				_ = emitStreamFailure(sse, sm, flush)
			}
			if !committed {
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
			if committed {
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
		if ev.Kind == lipapi.EventError && !committed {
			if isReserved && store != nil {
				cleanupContinuationReservation(store, scope, responseID)
				isReserved = false
			}
			status, typ, code, message := classifyCanonicalEventError(ev)
			writeWireError(w, status, typ, code, message)
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
			if committed {
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
			if !committed {
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
	if committed {
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

func emitStreamFailure(sse *proto.SSEWriter, sm *proto.StateMachine, flush func()) error {
	if sm.State() == proto.StateTerminal {
		if err := sse.WriteDONE(); err == nil {
			flush()
			return nil
		} else {
			return err
		}
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
	if err := sse.WriteDONE(); err != nil {
		return err
	}
	flush()
	return nil
}
