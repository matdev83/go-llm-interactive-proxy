package aggregate_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/aggregate"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestProjectLedgerRecord_PreservesTokensOmitsMoney(t *testing.T) {
	t.Parallel()
	f := fact("p1", 1, metering.FactKindCumulative, qty(metering.ComponentInputToken, 4), nil)
	f.Quantities = append(f.Quantities, metering.Quantity{
		Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 6, Present: true,
	})
	f.BackendID = "openai"
	f.Model = "gpt-test"
	f.Correlation.RequestID = "req-p"
	f.Correlation.AttemptID = "att-p"
	f.Money = &metering.MoneyObservation{NanoUnits: 99, Currency: "USD", Present: true, Source: metering.SourceProviderReported}
	rec, ok, err := aggregate.ProjectLedgerRecord(f)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if rec.InputTokens != 4 || rec.OutputTokens != 6 || rec.TotalTokens != 10 {
		t.Fatalf("rec=%+v", rec)
	}
	if rec.RequestID != "req-p" || rec.AttemptID != "att-p" {
		t.Fatalf("ids=%+v", rec)
	}
	// Money stays on journal side only — ledger.Record has no money fields.
}

func TestProjectLedgerRecord_SkipsUnavailable(t *testing.T) {
	t.Parallel()
	f := fact("u", 1, metering.FactKindUnavailable, nil, nil)
	f.Presence = metering.PresenceUnknown
	_, ok, err := aggregate.ProjectLedgerRecord(f)
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}
