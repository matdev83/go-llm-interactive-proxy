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

// CompactResourceIDSource issues identifiers for the standalone compaction
// resource. These are deliberately separate from proxy response IDs because a
// compact result is not a continuation record.
type CompactResourceIDSource interface {
	NewCompactResourceID() string
}

type systemCompactResourceIDSource struct{}

var _ CompactResourceIDSource = systemCompactResourceIDSource{}

func (systemCompactResourceIDSource) NewCompactResourceID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "comp_" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("comp_%d", time.Now().UnixNano())
}

const (
	maxCompactEvents    = 100_000
	maxCompactItems     = 10_000
	maxCompactTextBytes = 64 << 20
	maxCompactToolBytes = 128 << 20
)

func compactEnvelope(decoded *DecodedCompact, responseID string, now time.Time) proto.EnvelopeMetadata {
	return proto.EnvelopeMetadata{
		ResponseID:  responseID,
		CreatedAt:   now,
		CompletedAt: &now,
		Model:       decoded.Model,
	}
}

func collectCompact(ctx context.Context, stream lipapi.EventStream, envelope proto.EnvelopeMetadata, options lipapi.GenerationOptions, limits proto.Limits) (resource []byte, err error) {
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
	var (
		usage        proto.UsageStats
		textBytes    int
		toolBytes    int
		terminalSeen bool
	)

	for eventCount := 0; ; eventCount++ {
		if eventCount >= maxCompactEvents {
			return nil, errors.New("canonical response exceeded event limit")
		}

		if err := ctx.Err(); err != nil {
			return nil, err
		}

		ev, recvErr := stream.Recv(ctx)
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				if !terminalSeen {
					return nil, errors.New("canonical response ended without terminal event")
				}
				break
			}
			return nil, recvErr
		}

		if err := lipapi.ValidateEventEnvelope(&ev); err != nil {
			return nil, errors.New("canonical response contained an invalid event")
		}

		switch ev.Kind {
		case lipapi.EventTextDelta, lipapi.EventReasoningDelta:
			textBytes += len(ev.Delta)
			if textBytes > maxCompactTextBytes {
				return nil, errors.New("canonical response exceeded text limit")
			}
		case lipapi.EventToolCallArgsDelta:
			toolBytes += len(ev.Delta)
			if toolBytes > maxCompactToolBytes {
				return nil, errors.New("canonical response exceeded tool argument limit")
			}
		case lipapi.EventUsageDelta:
			usage.InputTokens += ev.InputTokens
			usage.OutputTokens += ev.OutputTokens
			usage.TotalTokens += ev.TotalTokens
			usage.CachedTokens += ev.CacheReadTokens
			usage.ReasoningTokens += ev.ReasoningTokens
		}

		// The compact wire resource has no error member, but the collector must
		// still propagate a terminal EventError to the HTTP handler so it can
		// return a failure status instead of a successful resource.
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

		if len(sm.Trajectory()) > maxCompactItems {
			return nil, errors.New("canonical response exceeded item limit")
		}

		if ev.Kind == lipapi.EventError || ev.Kind == lipapi.EventResponseFinished {
			if terminalSeen {
				return nil, errors.New("canonical response contained multiple terminal events")
			}
			terminalSeen = true
		}
	}

	if !terminalSeen || sm.State() != proto.StateTerminal {
		return nil, errors.New("canonical response ended without terminal event")
	}

	env := envelope
	env.Status = sm.Status()
	_, resourceJSON, buildErr := proto.BuildCompactResource(env, sm.Trajectory(), usage)
	if buildErr != nil {
		return nil, errors.New("failed to build compact resource")
	}
	return resourceJSON, nil
}
