package aggregate

import (
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/ledger"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// ProjectLedgerRecord maps a representable metering fact onto a legacy token
// ledger record. Money is intentionally omitted (token ledger is token-only);
// callers must keep money on the metering journal (requirement 13.3, 17.1, 17.4).
func ProjectLedgerRecord(f metering.Fact) (ledger.Record, bool, error) {
	if err := f.Validate(); err != nil {
		return ledger.Record{}, false, fmt.Errorf("metering/aggregate: %w", err)
	}
	switch f.Kind {
	case metering.FactKindUnavailable, metering.FactKindReservationEstimate:
		return ledger.Record{}, false, nil
	}
	req := strings.TrimSpace(f.Correlation.RequestID)
	if req == "" {
		req = strings.TrimSpace(f.StreamID)
	}
	attempt := strings.TrimSpace(f.Correlation.AttemptID)
	if attempt == "" {
		attempt = strings.TrimSpace(f.Correlation.BLegID)
	}
	if attempt == "" {
		attempt = strings.TrimSpace(f.FactID)
	}
	rec := ledger.Record{
		RequestID: req,
		AttemptID: attempt,
		Backend:   strings.TrimSpace(f.BackendID),
		Model:     strings.TrimSpace(f.Model),
		Plane:     lipapi.UsagePlaneProviderBillable,
		CreatedAt: f.RecordedAt,
		Metadata: lipapi.UsageAccountingMetadata{
			Plane:     lipapi.UsagePlaneProviderBillable,
			Source:    mapSource(f.Source),
			Authority: mapAuthority(f.Authority),
			Tokenizer: lipapi.TokenizerRef{Type: "metering", ID: "fact", Source: "metering"},
		},
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Unix(0, 0).UTC()
	}
	for _, q := range f.Quantities {
		if !q.Present {
			continue
		}
		v := int(q.Value)
		switch q.Component {
		case metering.ComponentInputToken:
			rec.InputTokens = v
		case metering.ComponentOutputToken:
			rec.OutputTokens = v
		case metering.ComponentCacheReadInputToken:
			rec.CacheReadTokens = v
		case metering.ComponentCacheWriteInputToken:
			rec.CacheWriteTokens = v
		case metering.ComponentReasoningOutputToken:
			rec.ReasoningTokens = v
		case metering.ComponentTotalToken:
			rec.TotalTokens = v
		}
	}
	if rec.TotalTokens == 0 {
		rec.TotalTokens = rec.InputTokens + rec.OutputTokens
	}
	if rec.Backend == "" {
		rec.Backend = "unknown"
	}
	if rec.Model == "" {
		rec.Model = "unknown"
	}
	return rec, true, nil
}

func mapSource(s metering.Source) lipapi.UsageSource {
	switch s {
	case metering.SourceProviderReported:
		return lipapi.UsageSourceProviderReported
	case metering.SourceEstimated:
		return lipapi.UsageSourceLocalEstimator
	case metering.SourceObserved:
		return lipapi.UsageSourceProxyAdjusted
	default:
		return lipapi.UsageSourceUnknown
	}
}

func mapAuthority(a metering.Authority) lipapi.UsageAuthority {
	switch a {
	case metering.AuthorityAuthoritative:
		return lipapi.UsageAuthorityAuthoritative
	default:
		return lipapi.UsageAuthorityAdvisory
	}
}
