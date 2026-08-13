package runtimebundle

import (
	"context"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	runtimecore "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
)

type billingLegObserver struct {
	log *slog.Logger
}

func billingLegObserverFor(log *slog.Logger) runtimecore.BillingLegObserver {
	if log == nil {
		return nil
	}
	return billingLegObserver{log: log}
}

func (o billingLegObserver) ObserveBillingLeg(ctx context.Context, record billing.LegUsageRecord) {
	o.log.LogAttrs(ctx, slog.LevelDebug, "lip.billing_b_leg",
		slog.String("a_leg_id", record.ALegID),
		slog.String("b_leg_id", record.BLegID),
		slog.Int("attempt_seq", record.Seq),
		slog.String("backend", record.BackendID),
		slog.String("provider", record.ProviderID),
		slog.String("model", record.ModelID),
		slog.String("outcome", string(record.Outcome)),
		slog.String("surfaced", string(record.Surfaced)),
		slog.Int64("input_tokens", record.Evidence.InputTokens.Value),
		slog.Bool("input_present", record.Evidence.InputTokens.Present),
		slog.Int64("output_tokens", record.Evidence.OutputTokens.Value),
		slog.Bool("output_present", record.Evidence.OutputTokens.Present),
		slog.Int64("cost_nano_units", record.Evidence.Cost.NanoUnits),
		slog.String("currency", record.Evidence.Cost.Currency),
		slog.Bool("cost_present", record.Evidence.Cost.Present),
		slog.String("evidence_source", string(record.Evidence.Source)),
		slog.String("evidence_authority", string(record.Evidence.Authority)),
		slog.String("evidence_dedupe_key", record.Evidence.DedupeKey),
	)
}
