package billing

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9Blocker5_COGSNormalizesSubjectCorrelationPlacementAcrossLegs(t *testing.T) {
	t.Parallel()

	callID := mustBillingCallID(t)
	subjectCarried := blocker5COGSObservation(t, callID, blocker5COGSSubjectCarried)
	correlationCarried := blocker5COGSObservation(t, callID, blocker5COGSCorrelationCarried)
	if subjectCarried.IdentityKey() != correlationCarried.IdentityKey() {
		t.Fatalf("carrier source keys differ: subject=%q correlation=%q", subjectCarried.IdentityKey(), correlationCarried.IdentityKey())
	}
	subjectHash, subjectErr := subjectCarried.ReplayFingerprint()
	correlationHash, correlationErr := correlationCarried.ReplayFingerprint()
	if subjectErr != nil || correlationErr != nil || subjectHash != correlationHash {
		t.Fatalf("carrier replay hashes differ: subject=%q err=%v correlation=%q err=%v", subjectHash, subjectErr, correlationHash, correlationErr)
	}
	if got, want := ObservationEvidenceHash(correlationCarried), ObservationEvidenceHash(subjectCarried); got != want {
		t.Fatalf("billing evidence hashes differ by carrier placement: got %q want %q", got, want)
	}
	legs := []CallLegUsageRecord{
		phase5V2Leg(t, callID, "blocker5-cogs-b-leg", subjectCarried),
		phase5V2Leg(t, callID, "blocker5-cogs-b-leg", correlationCarried),
	}
	result, err := AttributeOperatorCOGS(legs, nil, "USD")
	if err != nil {
		t.Fatalf("equivalent lineage placement rejected by COGS: %v", err)
	}
	if result.KnownSubtotal.Nano != 7_000_000_000 || len(result.IncludedLegKeys) != 1 {
		t.Fatalf("COGS result=%+v, want one included 7 USD charge", result)
	}
}

type blocker5COGSCarrier uint8

const (
	blocker5COGSSubjectCarried blocker5COGSCarrier = iota
	blocker5COGSCorrelationCarried
)

func blocker5COGSObservation(t *testing.T, callID BillingCallID, carrier blocker5COGSCarrier) metering.Observation {
	t.Helper()
	observation := phase5ChargeObservation(t, callID, "blocker5-cogs-b-leg", "blocker5-cogs-observation", "usage", stringPtrRepair9("7"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	observation.StreamID = "blocker5-cogs-stream"
	observation.Subject.TenantID = "blocker5-cogs-tenant"
	observation.Subject.CallID = callID.String()
	observation.Subject.ProviderAccountKey = "blocker5-cogs-account"
	observation.Correlation.TenantID = observation.Subject.TenantID
	observation.Correlation.ProviderAccountKey = observation.Subject.ProviderAccountKey
	if carrier == blocker5COGSSubjectCarried {
		observation.Correlation.TenantID = ""
		observation.Correlation.CallID = ""
		observation.Correlation.BillingCallID = ""
		observation.Correlation.ALegID = ""
		observation.Correlation.ProviderAccountKey = ""
	} else {
		observation.Subject.TenantID = ""
		observation.Subject.CallID = ""
		observation.Subject.BillingCallID = ""
		observation.Subject.ALegID = ""
		observation.Subject.ProviderAccountKey = ""
	}
	return observation
}
