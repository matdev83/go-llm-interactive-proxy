package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// promptStream maps ACP prompt NDJSON lines to lipapi.EventStream.
//
// Concurrency: one goroutine calls Recv at a time. Close may run concurrently with
// Recv blocked on scanner.Scan or network I/O; Close cancels the stream context and
// closes the response body so Scan unblocks.
// Context: Scan does not observe the per-Recv ctx; cancellation of that ctx alone does
// not unblock a blocked Scan—use Close (or parent cancellation that closes the body).
// See [lipapi.EventStream] cancellation notes.
//
// Context: ctx/cancel are derived from the Open parent via WithCancel and live for the
// stream lifetime (cancel on Close). Recv passes its per-call ctx to inbound server requests
// and JSON-RPC; line parsing uses the stored ctx so trace values from the parent propagate
// and cancellation aligns with Close/parent rather than an arbitrary Recv deadline.
type promptStream struct {
	mu sync.Mutex

	body        io.ReadCloser
	scanner     *bufio.Scanner
	cli         *client
	sessionID   string
	promptRPCID int64
	messageID   string

	mapper SessionUpdateMapperOptions
	srv    ServerRequestHandler

	cancelProfile CancelProfile

	ctx    context.Context
	cancel context.CancelFunc

	pending         stream.PendingEventQueue
	responseStarted bool
	messageStarted  bool
	after           bool
	closed          bool
}

func newPromptNDJSONStream(
	parent context.Context,
	body io.ReadCloser,
	cli *client,
	sessionID string,
	promptRPCID int64,
	messageID string,
	mapper SessionUpdateMapperOptions,
	srv ServerRequestHandler,
	cancelProfile CancelProfile,
	maxPending int,
) *promptStream {
	ctx, cancel := context.WithCancel(parent)
	s := &promptStream{
		body:          body,
		cli:           cli,
		sessionID:     sessionID,
		promptRPCID:   promptRPCID,
		messageID:     messageID,
		mapper:        mapper,
		srv:           serverHandlerOrDefault(srv),
		cancelProfile: cancelProfile,
		ctx:           ctx,
		cancel:        cancel,
		pending:       stream.NewPendingEventQueue(maxPending),
	}
	s.scanner = bufio.NewScanner(body)
	buf := make([]byte, 0, 64*1024)
	s.scanner.Buffer(buf, 1024*1024)
	return s
}

func (s *promptStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.cancel()
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}

func (s *promptStream) ensureResponseStartedLocked() error {
	if s.responseStarted {
		return nil
	}
	if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted}); err != nil {
		return err
	}
	s.responseStarted = true
	return nil
}

func (s *promptStream) ensureMessageStartedLocked() error {
	if s.messageStarted {
		return nil
	}
	if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted}); err != nil {
		return err
	}
	s.messageStarted = true
	return nil
}

func (s *promptStream) enqueueEventsLocked(evs []lipapi.Event) error {
	for _, e := range evs {
		switch e.Kind {
		case lipapi.EventTextDelta:
			if err := s.ensureResponseStartedLocked(); err != nil {
				return err
			}
			if err := s.ensureMessageStartedLocked(); err != nil {
				return err
			}
		case lipapi.EventReasoningDelta:
			if err := s.ensureResponseStartedLocked(); err != nil {
				return err
			}
			if err := s.ensureMessageStartedLocked(); err != nil {
				return err
			}
		case lipapi.EventError, lipapi.EventResponseFinished:
			if err := s.ensureResponseStartedLocked(); err != nil {
				return err
			}
		case lipapi.EventWarning:
			if err := s.ensureResponseStartedLocked(); err != nil {
				return err
			}
		default:
		}
		if err := s.pending.Push(e); err != nil {
			return err
		}
		if e.Kind == lipapi.EventResponseFinished {
			s.after = true
		}
	}
	return nil
}

func (s *promptStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		s.signalCancel()
		_ = s.Close()
		return lipapi.Event{}, err
	}
	for {
		s.mu.Lock()
		if ev, ok := s.pending.PopFront(); ok {
			s.mu.Unlock()
			return ev, nil
		}
		if s.after {
			s.mu.Unlock()
			return lipapi.Event{}, io.EOF
		}
		s.mu.Unlock()

		if !s.scanner.Scan() {
			if err := s.scanner.Err(); err != nil {
				return lipapi.Event{}, fmt.Errorf("acp: scan stream: %w", err)
			}
			s.mu.Lock()
			if !s.responseStarted {
				s.mu.Unlock()
				return lipapi.Event{}, io.ErrUnexpectedEOF
			}
			if !s.after {
				if err := s.ensureResponseStartedLocked(); err != nil {
					s.mu.Unlock()
					return lipapi.Event{}, err
				}
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseFinished}); err != nil {
					s.mu.Unlock()
					return lipapi.Event{}, err
				}
				s.after = true
				s.mu.Unlock()
				continue
			}
			s.mu.Unlock()
			return lipapi.Event{}, io.EOF
		}
		line := strings.TrimSpace(s.scanner.Text())
		if line == "" {
			continue
		}

		var probe map[string]any
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			return lipapi.Event{}, fmt.Errorf("acp: decode inbound line: %w", err)
		}

		if isInboundServerRequest(probe) {
			if err := s.handleInboundServerRequest(ctx, probe); err != nil {
				return lipapi.Event{}, fmt.Errorf("acp: handle inbound server request: %w", err)
			}
			continue
		}

		evs, err := parseNDJSONLine(s.ctx, s.mapper, line, s.promptRPCID)
		if err != nil {
			return lipapi.Event{}, fmt.Errorf("acp: parse NDJSON line: %w", err)
		}
		if len(evs) == 0 {
			continue
		}
		s.mu.Lock()
		if err := s.enqueueEventsLocked(evs); err != nil {
			s.mu.Unlock()
			return lipapi.Event{}, err
		}
		s.mu.Unlock()
	}
}

func (s *promptStream) handleInboundServerRequest(ctx context.Context, probe map[string]any) error {
	method, ok := probe["method"].(string)
	if !ok || strings.TrimSpace(method) == "" {
		return fmt.Errorf("acp: inbound JSON-RPC missing method")
	}
	idRaw, ok := probe["id"]
	if !ok || idRaw == nil {
		return nil
	}
	idBytes, err := json.Marshal(idRaw)
	if err != nil {
		return fmt.Errorf("acp: marshal inbound request id: %w", err)
	}
	paramsRaw := json.RawMessage(nil)
	if p, ok := probe["params"]; ok {
		b, err := json.Marshal(p)
		if err != nil {
			return fmt.Errorf("acp: marshal inbound request params: %w", err)
		}
		paramsRaw = b
	}
	res, err := s.srv.HandleServerRequest(ctx, method, json.RawMessage(idBytes), paramsRaw)
	if err != nil {
		return fmt.Errorf("acp: handle inbound server request method: %w", err)
	}
	body, err := replyServerRequestJSON(json.RawMessage(idBytes), res)
	if err != nil {
		return fmt.Errorf("acp: encode inbound server response: %w", err)
	}
	if err := s.cli.t.SendJSONRPC(ctx, body); err != nil {
		return fmt.Errorf("acp: send inbound server response: %w", err)
	}
	return nil
}

func (s *promptStream) signalCancel() {
	// WithoutCancel(s.ctx): the consumer ctx passed to Recv is already canceled when we run;
	// we still need a short cancel RPC to complete even if the stream ctx is canceled later.
	// Values from s.ctx (e.g. trace IDs) are preserved for the outbound request.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 2*time.Second)
	defer cancel()
	if err := s.cli.cancelSession(cctx, s.cancelProfile, s.sessionID, s.promptRPCID, s.messageID); err != nil {
		if log := s.cli.log; log != nil {
			log.Debug("acp: cancel session rpc failed", slog.String("error", diag.TruncErrDetail(err, 512)))
		}
	}
}
