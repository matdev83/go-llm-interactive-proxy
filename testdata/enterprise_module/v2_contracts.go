package main

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// These compile-only adapters prove that an external module can implement the
// neutral economics seams without importing internal packages or provider SDKs.
// They are compile-only adapters, not installed runtime raters/importers and
// do not perform rating, persistence, or provider calls.
type enterpriseV2Rater struct{}

func (enterpriseV2Rater) Rate(context.Context, economics.RatingInput) (economics.Valuation, error) {
	return economics.Valuation{}, nil
}

type enterpriseV2Quoter struct{}

func (enterpriseV2Quoter) Quote(context.Context, economics.QuoteInput) (economics.ExposureQuote, error) {
	return economics.ExposureQuote{}, nil
}

// enterpriseV2SnapshotContext is compile-only coverage for the additive V2
// replay fields. Historical VersionRef wire members remain owned by the
// economics package; external raters and quoters consume the content refs on
// their input/output envelopes instead.
func enterpriseV2SnapshotContext(in economics.RatingInput, quote economics.QuoteInput, valuation economics.Valuation, exposure economics.ExposureQuote) {
	_ = in.RaterContent
	_ = in.TariffContent
	_ = in.PolicyContent
	_ = in.QualifierSnapshotRef
	_ = quote.TariffContent
	_ = quote.PolicyContent
	_ = quote.QualifierSnapshotRef
	_ = valuation.RaterContent
	_ = valuation.TariffContent
	_ = valuation.PolicyContent
	_ = valuation.QualifierSnapshotRef
	_ = valuation.Payer
	_ = exposure.TariffContent
	_ = exposure.PolicyContent
	_ = exposure.QualifierSnapshotRef
}

var (
	_ metering.ObservationSink       = enterpriseV2ObservationSink{}
	_ economics.Rater                = enterpriseV2Rater{}
	_ economics.Quoter               = enterpriseV2Quoter{}
	_ economics.StatementImporter    = enterpriseV2StatementImporter{}
	_ economics.ReconciliationReader = enterpriseV2ReconciliationReader{}
)

type enterpriseV2ObservationSink struct{}

func (enterpriseV2ObservationSink) Append(context.Context, metering.Observation) error { return nil }

type enterpriseV2StatementImporter struct{}

func (enterpriseV2StatementImporter) Import(context.Context, economics.StatementBatch) (economics.ImportResult, error) {
	return economics.ImportResult{}, nil
}

type enterpriseV2ReconciliationReader struct{}

func (enterpriseV2ReconciliationReader) Query(context.Context, economics.ReconciliationQuery) (economics.ReconciliationPage, error) {
	return economics.ReconciliationPage{}, nil
}
