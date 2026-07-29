package product

import (
	"errors"
	"fmt"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
)

var (
	errRunSubOverflow = errors.New("cursorsdk: run subscription buffer full")
	errRunSubClosed   = errors.New("cursorsdk: run subscription closed")
	errRunIDConflict  = errors.New("cursorsdk: run id already in use")
)

type runSub struct {
	ch         chan *protocol.Frame
	seq        *protocol.RunSequencer
	generation int64
	mu         sync.Mutex
	closed     bool
	claimed    bool
	sendBound  bool
	err        error
}

func newRunSub(generation int64) *runSub {
	return &runSub{
		ch:         make(chan *protocol.Frame, 32),
		seq:        protocol.NewRunSequencer(),
		generation: generation,
	}
}

func (s *runSub) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *runSub) isClaimed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claimed
}

func (s *runSub) isSendBound() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sendBound
}

func (s *runSub) buffered() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ch)
}

func (s *runSub) markClaimed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimed = true
}

func (s *runSub) markSendBound() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sendBound = true
}

func (s *runSub) deliver(f *protocol.Frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		if s.err != nil {
			return s.err
		}
		return errRunSubClosed
	}
	if err := s.seq.Accept(f); err != nil {
		s.err = err
		s.closed = true
		close(s.ch)
		return err
	}
	select {
	case s.ch <- f:
		if protocol.IsTerminalKind(f.Kind) {
			s.closed = true
			close(s.ch)
		}
		return nil
	default:
		s.err = errRunSubOverflow
		s.closed = true
		close(s.ch)
		return errRunSubOverflow
	}
}

func (s *runSub) close() {
	s.closeWithErr(nil)
}

func (s *runSub) closeWithErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if err != nil && s.err == nil {
		s.err = err
	}
	s.closed = true
	close(s.ch)
}

func (s *runSub) TerminalErr() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return runSubTerminalFault(s.err)
}

func runSubTerminalFault(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errRunSubOverflow) {
		return NewBridgeFault(CodeBridgeProtocol, fmt.Errorf("%w: %w", ErrBridgeProtocol, errRunSubOverflow), "run subscription buffer full")
	}
	var pe *protocol.ProtocolError
	if errors.As(err, &pe) && pe != nil {
		return BridgeProtocolFault(pe.Class, pe.Message)
	}
	var bf *BridgeFault
	if errors.As(err, &bf) && bf != nil {
		return bf
	}
	return NewBridgeFault(CodeBridgeProtocol, fmt.Errorf("%w: %v", ErrBridgeProtocol, err), "")
}
