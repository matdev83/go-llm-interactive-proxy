package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// EconomicRevisionWorkAppender is the narrow durable queue seam used by the
// observation relay. Implementations must retain the immutable work marker and
// treat an exact identity replay as a no-op.
type EconomicRevisionWorkAppender interface {
	AppendEconomicRevisionWork(context.Context, EconomicRevisionWork) error
}

// EconomicRevisionWorkEvidenceAppender persists explicitly evidence-only work
// that drain never inventories as monetary (shadow, pure workers without a
// posting adapter). Implementations must not create pins or fences for these
// rows.
type EconomicRevisionWorkEvidenceAppender interface {
	AppendEvidenceEconomicRevisionWork(context.Context, EconomicRevisionWork) error
}

// EconomicRevisionWorkPostingAppender persists monetary provider work with an
// explicit posting owner (V1 pre-boundary, V2 authorized). Implementations
// must fence new V1 in draining/active and require v2_active for V2.
type EconomicRevisionWorkPostingAppender interface {
	AppendProviderPostingEconomicRevisionWork(context.Context, EconomicRevisionWork, string) error
}

// EconomicRevisionAdmittedOwnerResolver returns the call-scoped durable
// posting owner admitted for one monetary work item (customer pin owner for
// its account/call). Implementations must prefer this admitted owner over any
// unbound global marker read; when no admitted owner exists they fall back to
// the current marker (V2 in v2_active, else V1). The relay consumes it so
// fresh V2 observations are not misclassified as V1 after activation.
type EconomicRevisionAdmittedOwnerResolver interface {
	ResolveEconomicRevisionPostingOwner(context.Context, EconomicRevisionWork) (string, error)
}

// ObservationEconomicWorkBuilder converts an immutable observation set into
// independently recoverable customer/provider work. It performs no valuation,
// reconciliation, balance, exposure, or journal operation.
type ObservationEconomicWorkBuilder interface {
	BuildEconomicRevisionWork(context.Context, []metering.Observation) ([]EconomicRevisionWork, error)
}

// ObservationEconomicWorkInputFactory constructs the immutable rating input
// for one economic plane. The bridge supplies a cloned, deterministically
// ordered observation set and a stable B-leg subject.
type ObservationEconomicWorkInputFactory func(context.Context, metering.SubjectRef, []metering.Observation) (economics.PostUsageRatingInput, error)

// ObservationEconomicWorkBuilderConfig configures the provider/customer input
// factories. Provider-reported P is enabled by default because it needs no
// local tariff or policy snapshot. Customer-policy R is enabled when the
// composition root supplies an immutable snapshot-bound factory.
type ObservationEconomicWorkBuilderConfig struct {
	ProviderInput ObservationEconomicWorkInputFactory
	CustomerInput ObservationEconomicWorkInputFactory
	Now           func() time.Time
}

// observationEconomicWorkBuilder is intentionally stateless after
// construction. Snapshot material is captured by the input factories, while
// evidence itself remains the durable source of truth.
type observationEconomicWorkBuilder struct {
	providerInput ObservationEconomicWorkInputFactory
	customerInput ObservationEconomicWorkInputFactory
	now           func() time.Time
}

var _ ObservationEconomicWorkBuilder = (*observationEconomicWorkBuilder)(nil)

// NewObservationEconomicWorkBuilder constructs the production observation
// bridge. The default provider factory creates provider-reported P work; a nil
// customer factory deliberately leaves customer work disabled rather than
// fabricating an invalid derived snapshot context.
func NewObservationEconomicWorkBuilder(cfg ObservationEconomicWorkBuilderConfig) (ObservationEconomicWorkBuilder, error) {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	provider := cfg.ProviderInput
	if provider == nil {
		provider = defaultProviderReportedInput
	}
	return &observationEconomicWorkBuilder{providerInput: provider, customerInput: cfg.CustomerInput, now: now}, nil
}

func defaultProviderReportedInput(_ context.Context, subject metering.SubjectRef, observations []metering.Observation) (economics.PostUsageRatingInput, error) {
	return economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: subject, Scope: "b_leg", Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator},
		Observations: observations,
	}, nil
}

// BuildEconomicRevisionWork returns at most one work item per configured
// economic plane and stable B-leg. Input observation ordering is canonicalized
// before hashing; delivery order therefore cannot change work identity.
func (b *observationEconomicWorkBuilder) BuildEconomicRevisionWork(ctx context.Context, observations []metering.Observation) ([]EconomicRevisionWork, error) {
	if b == nil || b.providerInput == nil {
		return nil, fmt.Errorf("%w: observation work builder is incomplete", ErrInvalidEconomicRevision)
	}
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrInvalidEconomicRevision)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	groups := make(map[string]*observationEconomicGroup)
	for i, observation := range observations {
		if err := observation.Validate(); err != nil {
			return nil, fmt.Errorf("%w: observation %d: %v", ErrInvalidEconomicRevision, i, err)
		}
		if !economicObservationCanLinkToBLeg(observation) {
			// Economic inference work is request-scoped. A statement-line
			// observation may participate only when its authenticated
			// correlation carries one unambiguous B-leg; account-window and
			// unrelated statement evidence remain on their native reconciliation
			// path until an explicit allocation exists.
			continue
		}
		subject := stableEconomicSubjectForObservation(observation)
		if subject.BLegID == "" {
			continue
		}
		key := stableEconomicSubjectKey(subject)
		group := groups[key]
		if group == nil {
			group = &observationEconomicGroup{subject: subject}
			groups[key] = group
		}
		if providerObservationForRevision(observation) {
			group.provider = append(group.provider, observation.Clone())
		}
		// The incremental customer plane is quantity-backed B-leg inference.
		// Charge-only provider corrections belong exclusively to P; routing
		// them into R would manufacture an incomplete customer revision even
		// though independent-retail settlement remains frozen until closure.
		if isRetailQuantityObservation(observation) {
			group.customer = append(group.customer, observation.Clone())
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	works := make([]EconomicRevisionWork, 0, len(keys)*2)
	for _, key := range keys {
		group := groups[key]
		if len(group.provider) > 0 {
			work, err := b.makeWork(ctx, EconomicQueueProvider, group.subject, group.provider, b.providerInput)
			if err != nil {
				return nil, fmt.Errorf("%w: provider plane: %v", ErrInvalidEconomicRevision, err)
			}
			works = append(works, work)
		}
		if len(group.customer) > 0 && b.customerInput != nil {
			work, err := b.makeWork(ctx, EconomicQueueCustomer, group.subject, group.customer, b.customerInput)
			if err != nil {
				return nil, fmt.Errorf("%w: customer plane: %v", ErrInvalidEconomicRevision, err)
			}
			works = append(works, work)
		}
	}
	sort.Slice(works, func(i, j int) bool {
		if works[i].HeadKey != works[j].HeadKey {
			return works[i].HeadKey < works[j].HeadKey
		}
		return works[i].Queue < works[j].Queue
	})
	return works, nil
}

type observationEconomicGroup struct {
	subject  metering.SubjectRef
	provider []metering.Observation
	customer []metering.Observation
}

func providerObservationForRevision(observation metering.Observation) bool {
	if observation.Origin == metering.OriginProvider && observation.Subject.Kind == metering.SubjectBLeg {
		return len(observation.Charges) > 0 || observation.Authority == metering.AuthorityUnavailableClaim
	}
	return isVerifiedStatementObservation(observation) && stableEconomicSubjectForObservation(observation).BLegID != ""
}

// economicObservationCanLinkToBLeg is the relay/build boundary for
// request-scoped economic work. A native statement line is accepted only when
// its trusted correlation identifies exactly one B-leg; accepting a bare
// account/window statement here would fabricate a Cartesian allocation.
func economicObservationCanLinkToBLeg(observation metering.Observation) bool {
	switch observation.Subject.Kind {
	case metering.SubjectBLeg:
		return observation.Subject.BLegID != ""
	case metering.SubjectStatementLine:
		return isVerifiedStatementObservation(observation) && stableEconomicSubjectForObservation(observation).BLegID != ""
	default:
		return false
	}
}

func isVerifiedStatementObservation(observation metering.Observation) bool {
	return observation.Origin == metering.OriginStatement &&
		observation.Acquisition == metering.AcquisitionStatementImporter &&
		observation.Authority == metering.AuthorityVerifiedStatement &&
		observation.Subject.Kind == metering.SubjectStatementLine &&
		observation.Subject.StatementLineID != ""
}

func (b *observationEconomicWorkBuilder) makeWork(ctx context.Context, queue EconomicQueue, subject metering.SubjectRef, observations []metering.Observation, factory ObservationEconomicWorkInputFactory) (EconomicRevisionWork, error) {
	canonical, err := canonicalBridgeObservations(observations)
	if err != nil {
		return EconomicRevisionWork{}, err
	}
	input, err := factory(ctx, subject, cloneObservations(canonical))
	if err != nil {
		return EconomicRevisionWork{}, err
	}
	input.Subject = subject
	input.Observations = cloneObservations(canonical)
	input.ObservationRefs = nil
	input.InputSetHash = ""
	if input.AsOf.IsZero() {
		for _, observation := range canonical {
			if observation.ObservedAt.After(input.AsOf) {
				input.AsOf = observation.ObservedAt
			}
		}
	}
	createdAt := input.AsOf
	if createdAt.IsZero() {
		createdAt = b.now()
	}
	if createdAt.IsZero() {
		createdAt = time.Unix(0, 0).UTC()
	}
	work := EconomicRevisionWork{
		Queue: queue, HeadKey: economicObservationHeadKey(subject), Subject: subject,
		EvidenceRevision: maxObservationRevision(canonical), Input: input, CreatedAt: createdAt,
	}
	normalized, err := work.Normalize()
	if err != nil {
		return EconomicRevisionWork{}, err
	}
	return normalized, nil
}

func canonicalBridgeObservations(observations []metering.Observation) ([]metering.Observation, error) {
	cloned := cloneObservations(observations)
	sort.Slice(cloned, func(i, j int) bool {
		left, right := cloned[i], cloned[j]
		if left.IdentityKey() != right.IdentityKey() {
			return left.IdentityKey() < right.IdentityKey()
		}
		return left.Fingerprint() < right.Fingerprint()
	})
	unique := cloned[:0]
	type seenObservation struct{ replayHash string }
	seen := make(map[string]seenObservation, len(cloned))
	for _, observation := range cloned {
		identity := observation.IdentityKey()
		replayHash, err := observation.ReplayFingerprint()
		if err != nil {
			return nil, err
		}
		if prior, found := seen[identity]; found {
			if prior.replayHash != replayHash {
				return nil, fmt.Errorf("conflicting duplicate observation identity %q", identity)
			}
			continue
		}
		seen[identity] = seenObservation{replayHash: replayHash}
		unique = append(unique, observation)
	}
	return unique, nil
}

func cloneObservations(observations []metering.Observation) []metering.Observation {
	if len(observations) == 0 {
		return nil
	}
	out := make([]metering.Observation, len(observations))
	for i, observation := range observations {
		out[i] = observation.Clone()
	}
	return out
}

func maxObservationRevision(observations []metering.Observation) uint64 {
	var revision uint64
	for _, observation := range observations {
		if observation.Revision > revision {
			revision = observation.Revision
		}
	}
	return revision
}

func stableEconomicSubject(subject metering.SubjectRef) metering.SubjectRef {
	return metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: subject.StoreID, TenantID: subject.TenantID, AccountID: subject.AccountID,
		ALegID: subject.ALegID, RequestID: subject.RequestID, BillingCallID: subject.BillingCallID, CallID: subject.CallID,
		BLegID: subject.BLegID, AttemptID: subject.AttemptID, AttemptSeq: subject.AttemptSeq,
		ProviderAccountKey: subject.ProviderAccountKey,
	}
}

// stableEconomicSubjectForObservation merges the two trusted lineage carriers
// before deriving a work scope. Observation validation rejects contradictory
// values, but permits a field to be carried by correlation only; dropping
// such a value here would combine otherwise isolated provider/customer work.
func stableEconomicSubjectForObservation(observation metering.Observation) metering.SubjectRef {
	subject := observation.Subject
	correlation := observation.Correlation
	subject.TenantID = firstNonEmptyBridge(subject.TenantID, correlation.TenantID)
	subject.RequestID = firstNonEmptyBridge(subject.RequestID, correlation.RequestID)
	subject.CallID = firstNonEmptyBridge(subject.CallID, correlation.CallID)
	subject.BillingCallID = firstNonEmptyBridge(subject.BillingCallID, correlation.BillingCallID)
	subject.ALegID = firstNonEmptyBridge(subject.ALegID, correlation.ALegID)
	subject.BLegID = firstNonEmptyBridge(subject.BLegID, correlation.BLegID)
	subject.AttemptID = firstNonEmptyBridge(subject.AttemptID, correlation.AttemptID)
	if subject.AttemptSeq == 0 {
		subject.AttemptSeq = correlation.AttemptSeq
	}
	subject.SubmissionID = firstNonEmptyBridge(subject.SubmissionID, correlation.SubmissionID)
	subject.ProviderAccountKey = firstNonEmptyBridge(subject.ProviderAccountKey, correlation.ProviderAccountKey)
	subject.ProviderRequestID = firstNonEmptyBridge(subject.ProviderRequestID, correlation.ProviderRequestID)
	subject.ProviderChargeID = firstNonEmptyBridge(subject.ProviderChargeID, correlation.ProviderChargeID)
	subject.ResourceID = firstNonEmptyBridge(subject.ResourceID, correlation.ResourceID)
	subject.PeriodID = firstNonEmptyBridge(subject.PeriodID, correlation.PeriodID)
	return stableEconomicSubject(subject)
}

func firstNonEmptyBridge(left, right string) string {
	if left != "" {
		return left
	}
	return right
}

func stableEconomicSubjectKey(subject metering.SubjectRef) string {
	stable := stableEconomicSubject(subject)
	encoded := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d", stable.StoreID, stable.TenantID, stable.AccountID, stable.ProviderAccountKey, stable.ALegID, stable.RequestID, stable.BillingCallID, stable.CallID, stable.BLegID, stable.AttemptSeq)
	return encoded + "\x00" + stable.AttemptID
}

func economicObservationHeadKey(subject metering.SubjectRef) string {
	seed := stableEconomicSubjectKey(subject)
	digest := sha256.Sum256([]byte("economic-observation-head:v1\x00" + seed))
	return "b-leg-economic:v1:" + hex.EncodeToString(digest[:])
}

// BuildCustomerPolicyObservationInput creates the snapshot-bound R input used
// by stock composition. Snapshot content hashes are derived from the immutable
// policy/tariff bodies, so replay does not depend on a live mutable catalog.
func BuildCustomerPolicyObservationInput(subject metering.SubjectRef, observations []metering.Observation, policy ChargePolicy, tariff economics.TariffSnapshot) (economics.PostUsageRatingInput, error) {
	if err := policy.Validate(); err != nil {
		return economics.PostUsageRatingInput{}, fmt.Errorf("customer policy: %w", err)
	}
	canonicalTariff, err := tariff.Canonical()
	if err != nil {
		return economics.PostUsageRatingInput{}, fmt.Errorf("customer tariff: %w", err)
	}
	if err := subject.Validate(); err != nil {
		return economics.PostUsageRatingInput{}, fmt.Errorf("customer subject: %w", err)
	}
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveCustomer, Basis: economics.BasisCustomerPolicy,
		Subject: subject, Scope: "b_leg", Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: subject.AccountID},
		Observations: cloneObservations(observations), EffectiveQualifiers: append([]metering.Dimension(nil), canonicalTariff.EffectiveQualifiers...),
		Rater: canonicalTariff.Ref, RaterContent: cloneSnapshotContent(&canonicalTariff.Content),
		Tariff: canonicalTariff.Ref, TariffContent: cloneSnapshotContent(&canonicalTariff.Content),
		Policy:        economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: policy.Ref.ID, Version: policy.Ref.Version, EffectiveAt: policy.Ref.EffectiveAt, FetchedAt: policy.Ref.FetchedAt}, PolicyID: policy.Ref.ID},
		PolicyContent: retailPolicyContent(policy),
	}
	input.QualifierSnapshotRef = retailQualifierContent(canonicalTariff, input.EffectiveQualifiers)
	return input, nil
}
