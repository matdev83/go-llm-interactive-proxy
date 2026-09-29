package extensions

import (
	"context"
	"log/slog"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification"
)

// RunSessionClassificationStage executes one metadata-only classifier fail-open.
// It never changes request content or authoritative session identity.
func RunSessionClassificationStage(
	ctx context.Context,
	log *slog.Logger,
	obs StageMetrics,
	classifier sessionclassification.Classifier,
	in sessionclassification.Input,
) session.Classification {
	current := in.Session.Classification
	if classifier == nil {
		return current
	}
	start := time.Now()
	outcome := "ok"
	defer func() {
		if obs != nil {
			obs.ObserveStage(feature.StageIDSessionClassification, outcome, time.Since(start).Seconds())
		}
	}()
	got, err := safety.CallValue(safety.BoundaryExtension, "session_classification", func() (session.Classification, error) {
		return classifier.Classify(ctx, in)
	})
	if err != nil {
		outcome = "fail_open"
		if obs != nil {
			obs.IncFailOpenSkip(feature.StageIDSessionClassification)
		}
		if log != nil {
			log.DebugContext(ctx, "session_classification: classifier error (fail-open)", "classifier", classifier.ID(), "error", err)
		}
		return current
	}
	if err := got.Validate(); err != nil {
		outcome = "invalid_fail_open"
		if obs != nil {
			obs.IncFailOpenSkip(feature.StageIDSessionClassification)
		}
		if log != nil {
			log.DebugContext(ctx, "session_classification: invalid result (fail-open)", "classifier", classifier.ID(), "error", err)
		}
		return current
	}
	if current.IsCodingAgent() && !got.IsCodingAgent() {
		return current
	}
	return got
}
