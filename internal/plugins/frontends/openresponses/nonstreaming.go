package openresponses

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	proto "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/protocols/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ResponseIDSource issues proxy-owned response identifiers.
type ResponseIDSource interface {
	NewResponseID() string
}

// ResponseClock supplies proxy-owned response timestamps.
type ResponseClock interface {
	Now() time.Time
}

type systemResponseIDSource struct{}

var _ ResponseIDSource = systemResponseIDSource{}

func (systemResponseIDSource) NewResponseID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "resp_" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("resp_%d", time.Now().UnixNano())
}

type systemResponseClock struct{}

var _ ResponseClock = systemResponseClock{}

func (systemResponseClock) Now() time.Time { return time.Now() }

const (
	maxNonStreamingEvents    = 100_000
	maxNonStreamingItems     = 10_000
	maxNonStreamingTextBytes = 64 << 20
	maxNonStreamingToolBytes = 128 << 20
)

func collectNonStreaming(ctx context.Context, stream lipapi.EventStream, envelope proto.EnvelopeMetadata, options lipapi.GenerationOptions, limits proto.Limits) (resource []byte, err error) {
	if stream == nil {
		return nil, errors.New("nil canonical event stream")
	}
	defer func() {
		if closeErr := stream.Close(); closeErr != nil && err == nil {
			err = errors.New("canonical response stream close failed")
			resource = nil
		}
	}()

	sm := proto.NewStateMachine(envelope, options, effectiveProtocolLimits(limits))
	var textBytes, toolBytes int
	for eventCount := 0; ; eventCount++ {
		if eventCount >= maxNonStreamingEvents {
			return nil, errors.New("canonical response exceeded event limit")
		}
		ev, err := stream.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("canonical response ended without terminal event")
			}
			return nil, err
		}
		if err := lipapi.ValidateEventEnvelope(&ev); err != nil {
			return nil, errors.New("canonical response contained an invalid event")
		}
		switch ev.Kind {
		case lipapi.EventTextDelta, lipapi.EventReasoningDelta:
			textBytes += len(ev.Delta)
			if textBytes > maxNonStreamingTextBytes {
				return nil, errors.New("canonical response exceeded text limit")
			}
		case lipapi.EventToolCallArgsDelta:
			toolBytes += len(ev.Delta)
			if toolBytes > maxNonStreamingToolBytes {
				return nil, errors.New("canonical response exceeded tool argument limit")
			}
		}

		if ev.Kind == lipapi.EventError {
			if ev.ErrorCode == "" {
				ev.ErrorCode = "backend_error"
			}
			if _, err := sm.ProcessCanonicalEvent(ev); err != nil {
				return nil, errors.New("canonical response sequence was invalid")
			}
			return nil, lipapi.NewStreamError(ev.ErrorCode, ev.ErrorMessage)
		}
		if _, err := sm.ProcessCanonicalEvent(ev); err != nil {
			return nil, errors.New("canonical response sequence was invalid")
		}
		if len(sm.Trajectory()) > maxNonStreamingItems {
			return nil, errors.New("canonical response exceeded item limit")
		}
		if ev.Kind == lipapi.EventError || ev.Kind == lipapi.EventResponseFinished {
			_, resourceJSON, buildErr := sm.AccumulateResource()
			if buildErr != nil {
				return nil, errors.New("failed to build response resource")
			}
			return resourceJSON, nil
		}
	}
}

func nonStreamingEnvelope(decoded *DecodedCreate, responseID string, now time.Time) proto.EnvelopeMetadata {
	return proto.EnvelopeMetadata{
		ResponseID:         responseID,
		PreviousResponseID: decoded.PreviousResponseID,
		CreatedAt:          now,
		CompletedAt:        &now,
		Model:              decoded.Model,
		Store:              &decoded.Store,
	}
}
