package billing

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

func TestCompareEconomicEvidenceSetsUsesReferenceContainment(t *testing.T) {
	t.Parallel()
	ref := func(id, hash string) metering.ObservationRef {
		return metering.ObservationRef{StoreID: "store", ObservationID: id, Revision: 1, PayloadHash: hash}
	}
	a := ref("a", strings.Repeat("a", 64))
	b := ref("b", strings.Repeat("b", 64))
	c := ref("c", strings.Repeat("c", 64))
	tests := []struct {
		name      string
		current   []metering.ObservationRef
		candidate []metering.ObservationRef
		want      EconomicEvidenceSetRelation
	}{
		{name: "exact set replay", current: []metering.ObservationRef{b, a}, candidate: []metering.ObservationRef{a, b, a}, want: EconomicEvidenceSetEqual},
		{name: "strict superset", current: []metering.ObservationRef{a, b}, candidate: []metering.ObservationRef{a, b, c}, want: EconomicEvidenceSetCandidateSuperset},
		{name: "strict subset", current: []metering.ObservationRef{a, b}, candidate: []metering.ObservationRef{a}, want: EconomicEvidenceSetCandidateSubset},
		{name: "incomparable", current: []metering.ObservationRef{a, b}, candidate: []metering.ObservationRef{a, c}, want: EconomicEvidenceSetIncomparable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := CompareEconomicEvidenceSets(test.current, test.candidate)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestCompareEconomicEvidenceSetsRejectsConflictingReferencePayloads(t *testing.T) {
	t.Parallel()
	left := metering.ObservationRef{StoreID: "store", ObservationID: "same", Revision: 2, PayloadHash: strings.Repeat("a", 64)}
	right := left
	right.PayloadHash = strings.Repeat("b", 64)
	_, err := CompareEconomicEvidenceSets([]metering.ObservationRef{left, right}, nil)
	require.Error(t, err)
}
