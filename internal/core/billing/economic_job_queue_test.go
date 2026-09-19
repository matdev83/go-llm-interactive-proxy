package billing

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

func economicJobTestWork(t *testing.T, queue EconomicQueue, revision uint64, label string, quantity string) EconomicRevisionWork {
	t.Helper()
	observation := phase9Observation(t, "job-"+queue.String()+"-"+label, metering.OriginProvider, metering.ComponentKey{
		Direction: metering.DirectionOutput,
		Component: metering.ComponentTextToken,
		Unit:      metering.UnitToken,
		SchemaID:  "economic-job-queue-v1",
	}, quantity)
	input := phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{observation})
	return EconomicRevisionWork{
		Queue: queue, HeadKey: "job-head-" + queue.String() + "-" + label,
		Subject: input.Subject, EvidenceRevision: revision, Input: input,
		CreatedAt: time.Unix(1_700_050_000+int64(revision), 0).UTC(),
	}
}

func economicJobTestDependency(t *testing.T, queue EconomicQueue, revision uint64, label string, quantity string) EconomicJobDependency {
	t.Helper()
	work := economicJobTestWork(t, queue, revision, label, quantity)
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	return EconomicJobDependency{
		Kind: EconomicWorkKindForQueue(queue), Queue: normalized.Queue, HeadKey: normalized.HeadKey,
		EvidenceRevision: identity.EvidenceRevision, InputSetHash: identity.InputSetHash,
	}
}

func economicJobTestReconciliation(t *testing.T, deps ...EconomicJobDependency) EconomicRevisionWork {
	t.Helper()
	work := economicJobTestWork(t, EconomicQueueProvider, 9, "reconciliation-evidence", "9")
	work.Kind = EconomicWorkKindReconciliation
	work.HeadKey = "job-head-reconciliation"
	work.Dependencies = deps
	return work
}

func TestEconomicJobQueueVocabularyIsClosed(t *testing.T) {
	t.Parallel()
	require.Len(t, AllEconomicWorkKinds(), 3)
	for _, kind := range AllEconomicWorkKinds() {
		require.NoError(t, kind.Validate())
	}
	require.ErrorIs(t, EconomicWorkKind("mystery").Validate(), ErrInvalidEconomicRevision)
	customerQueue, err := EconomicWorkKindCustomerRating.Queue()
	require.NoError(t, err)
	require.Equal(t, EconomicQueueCustomer, customerQueue)
	providerQueue, err := EconomicWorkKindProviderRating.Queue()
	require.NoError(t, err)
	require.Equal(t, EconomicQueueProvider, providerQueue)
	reconciliationQueue, err := EconomicWorkKindReconciliation.Queue()
	require.NoError(t, err)
	require.Equal(t, EconomicQueueProvider, reconciliationQueue)
	require.Equal(t, EconomicWorkKindCustomerRating, EconomicWorkKindForQueue(EconomicQueueCustomer))
	require.Equal(t, EconomicWorkKindProviderRating, EconomicWorkKindForQueue(EconomicQueueProvider))

	require.Len(t, AllEconomicWorkStatuses(), 4)
	for _, status := range AllEconomicWorkStatuses() {
		require.NoError(t, status.Validate())
	}
	require.False(t, EconomicWorkStatusPending.IsTerminal())
	require.False(t, EconomicWorkStatusProcessing.IsTerminal())
	require.True(t, EconomicWorkStatusCompleted.IsTerminal())
	require.True(t, EconomicWorkStatusFailed.IsTerminal())
	require.ErrorIs(t, EconomicWorkStatus("stuck").Validate(), ErrInvalidEconomicRevision)

	require.NotEmpty(t, AllEconomicWorkReasons())
	for _, reason := range AllEconomicWorkReasons() {
		require.NoError(t, reason.Validate())
	}
	require.ErrorIs(t, EconomicWorkReason("why").Validate(), ErrInvalidEconomicRevision)

	long := strings.Repeat("x", MaxEconomicWorkReasonLength*4)
	bounded := BoundedEconomicWorkReasonText("  " + long + "  ")
	require.Len(t, []rune(bounded), MaxEconomicWorkReasonLength)
	require.Empty(t, BoundedEconomicWorkReasonText("   "))
}

func TestEconomicJobWorkKindBindsToQueue(t *testing.T) {
	t.Parallel()
	for _, queue := range []EconomicQueue{EconomicQueueCustomer, EconomicQueueProvider} {
		work := economicJobTestWork(t, queue, 1, "kind-"+queue.String(), "1")
		normalized, err := work.Normalize()
		require.NoError(t, err)
		require.Equal(t, EconomicWorkKindForQueue(queue), normalized.Kind)
	}

	mismatch := economicJobTestWork(t, EconomicQueueCustomer, 1, "kind-mismatch", "2")
	mismatch.Kind = EconomicWorkKindProviderRating
	_, err := mismatch.Normalize()
	require.ErrorIs(t, err, ErrInvalidEconomicRevision)

	ratingWithDependency := economicJobTestWork(t, EconomicQueueProvider, 1, "rating-with-dependency", "3")
	ratingWithDependency.Dependencies = []EconomicJobDependency{
		economicJobTestDependency(t, EconomicQueueCustomer, 1, "dependency-source", "4"),
	}
	_, err = ratingWithDependency.Normalize()
	require.ErrorIs(t, err, ErrInvalidEconomicRevision)
}

func TestEconomicJobReconciliationRequiresRatedDependencies(t *testing.T) {
	t.Parallel()
	noDependency := economicJobTestReconciliation(t)
	_, err := noDependency.Normalize()
	require.ErrorIs(t, err, ErrInvalidEconomicRevision)

	reconciliationDependency := economicJobTestDependency(t, EconomicQueueProvider, 2, "chained", "5")
	reconciliationDependency.Kind = EconomicWorkKindReconciliation
	chained := economicJobTestReconciliation(t, reconciliationDependency)
	_, err = chained.Normalize()
	require.ErrorIs(t, err, ErrInvalidEconomicRevision)

	tooMany := economicJobTestReconciliation(t)
	for i := 0; i <= MaxEconomicJobDependencies; i++ {
		tooMany.Dependencies = append(tooMany.Dependencies, economicJobTestDependency(t, EconomicQueueProvider, uint64(i+1), "many-"+strconv.Itoa(i), "6"))
	}
	_, err = tooMany.Normalize()
	require.ErrorIs(t, err, ErrInvalidEconomicRevision)

	// A dependency that repeats the work's own queue/head/revision/input hash
	// would make the reconciliation depend on itself.
	selfBase := economicJobTestWork(t, EconomicQueueProvider, 5, "self", "7")
	selfDependency := economicJobTestDependency(t, EconomicQueueProvider, 5, "self", "7")
	selfWork := selfBase
	selfWork.Kind = EconomicWorkKindReconciliation
	selfWork.Dependencies = []EconomicJobDependency{selfDependency}
	_, err = selfWork.Normalize()
	require.ErrorIs(t, err, ErrInvalidEconomicRevision)
}

func TestEconomicJobDependencyHashChangesWorkIdentity(t *testing.T) {
	t.Parallel()
	rating := economicJobTestWork(t, EconomicQueueProvider, 3, "dep-source", "8")
	ratingIdentity, err := rating.Identity()
	require.NoError(t, err)
	require.Empty(t, ratingIdentity.DependenciesHash)

	dependency := economicJobTestDependency(t, EconomicQueueProvider, 3, "dep-source", "8")
	reconciliation := economicJobTestReconciliation(t, dependency)
	reconciliationIdentity, err := reconciliation.Identity()
	require.NoError(t, err)
	require.NotEmpty(t, reconciliationIdentity.DependenciesHash)
	require.NotEqual(t, ratingIdentity.Key(), reconciliationIdentity.Key())

	otherDependency := economicJobTestDependency(t, EconomicQueueCustomer, 3, "dep-source", "8")
	otherReconciliation := economicJobTestReconciliation(t, otherDependency)
	otherIdentity, err := otherReconciliation.Identity()
	require.NoError(t, err)
	require.NotEqual(t, reconciliationIdentity.Key(), otherIdentity.Key())

	// Dependency ordering is canonical and an exact duplicate is collapsed, so
	// transport/insertion order cannot change the actionable identity.
	forward := economicJobTestReconciliation(t, dependency, otherDependency)
	reversed := economicJobTestReconciliation(t, otherDependency, dependency)
	duplicated := economicJobTestReconciliation(t, dependency, otherDependency, dependency)
	forwardIdentity, err := forward.Identity()
	require.NoError(t, err)
	reversedIdentity, err := reversed.Identity()
	require.NoError(t, err)
	duplicatedIdentity, err := duplicated.Identity()
	require.NoError(t, err)
	require.Equal(t, forwardIdentity.Key(), reversedIdentity.Key())
	require.Equal(t, forwardIdentity.Key(), duplicatedIdentity.Key())

	// An invalid dependency hash is rejected before any key is derived.
	invalid := reconciliationIdentity
	invalid.DependenciesHash = "NOT-A-HASH"
	require.ErrorIs(t, invalid.Validate(), ErrInvalidEconomicRevision)
}

func TestEconomicJobScopeAndDependenciesAreCanonical(t *testing.T) {
	t.Parallel()
	work := economicJobTestWork(t, EconomicQueueProvider, 4, "scope", "10")
	normalized, err := work.Normalize()
	require.NoError(t, err)
	require.Equal(t, metering.SubjectBLeg, normalized.Subject.Kind)
	require.Equal(t, work.Subject.StoreID, normalized.Subject.StoreID)
	require.Equal(t, work.Subject.AccountID, normalized.Subject.AccountID)
	require.Equal(t, work.Subject.BillingCallID, normalized.Subject.BillingCallID)
	require.Equal(t, work.Subject.BLegID, normalized.Subject.BLegID)

	dependency := economicJobTestDependency(t, EconomicQueueProvider, 4, "scope", "10")
	require.NoError(t, dependency.Validate())
	output, err := dependency.OutputIdentity()
	require.NoError(t, err)
	require.Equal(t, dependency.Key(), output.Key())
	require.NotEmpty(t, output.ValuationKey())
	require.NotEqual(t, output.Key(), output.ValuationKey())

	second := economicJobTestDependency(t, EconomicQueueProvider, 4, "scope", "10")
	require.True(t, dependency.Equal(second))
	changed := economicJobTestDependency(t, EconomicQueueProvider, 5, "scope", "10")
	require.False(t, dependency.Equal(changed))
}
