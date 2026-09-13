package metering

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipsdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// ProviderEvidenceBinder is implemented by an in-process provider stream that
// captured provider-shaped usage before the runtime assigned the trusted
// request/B-leg identity. The binding is deliberately one-way: provider data
// cannot choose its store, call, or B-leg subject.
type ProviderEvidenceBinder interface {
	BindEconomicEvidence(ObservationIdentity)
}

// ProviderEvidenceDraft is the provider-neutral portion of one provider
// observation. Provider adapters own construction of measures, charges and
// safe evidence; this type only makes the runtime identity boundary explicit.
// A draft cannot be drained as V2 until a trusted B-leg binding is supplied.
type ProviderEvidenceDraft struct {
	SourceEventKey     string
	Revision           uint64
	StreamID           string
	Sequence           uint64
	Origin             string
	Acquisition        string
	Authority          string
	Perspective        lipsdkmetering.EconomicPerspective
	Boundary           lipsdkmetering.Boundary
	Lifecycle          lipsdkmetering.LifecycleScope
	Semantics          string
	ProviderAccountKey string
	ProviderRequestID  string
	ProviderChargeID   string
	Measures           []lipsdkmetering.Measure
	Charges            []lipsdkmetering.ReportedCharge
	Supersedes         []lipsdkmetering.ObservationRef
	Evidence           []lipsdkmetering.SafeEvidenceField
	ObservedAt         time.Time
	ReceivedAt         time.Time
	Coverage           string
	CoverageReason     string

	// sourceRevision is provider supplied when Revision was non-zero at Add
	// time. The exported Revision may later be advanced by the host to create
	// an immutable local observation revision, so replay identity must retain
	// this original source value separately.
	sourceRevision uint64
}

// ProviderEvidenceBuffer retains bounded, immutable provider drafts until the
// executor binds the concrete B-leg. It deduplicates exact replay of one
// source payload while preserving changed payloads as revisions.
type ProviderEvidenceBuffer struct {
	mu                          sync.Mutex
	identity                    ObservationIdentity
	bound                       bool
	drafts                      []ProviderEvidenceDraft
	lastFingerprint             map[string]string
	fingerprintOrder            []string
	explicitRevisionFingerprint map[string]string
	explicitRevisionOrder       []string
	lastRevision                map[string]uint64
	lastObservation             map[string]lipsdkmetering.Observation
	retained                    int
}

const maxProviderEvidenceDrafts = 256

// NewProviderEvidenceBuffer creates a stream-local provider evidence buffer.
func NewProviderEvidenceBuffer() *ProviderEvidenceBuffer {
	return &ProviderEvidenceBuffer{
		lastFingerprint:             make(map[string]string),
		explicitRevisionFingerprint: make(map[string]string),
		lastRevision:                make(map[string]uint64),
		lastObservation:             make(map[string]lipsdkmetering.Observation),
	}
}

// Add appends one provider-owned draft. Exact replay under the same source key
// is ignored; changed content is retained and receives a later revision when
// the draft does not provide one.
func (b *ProviderEvidenceBuffer) Add(draft ProviderEvidenceDraft) {
	if b == nil || strings.TrimSpace(draft.SourceEventKey) == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.retained >= maxProviderEvidenceDrafts {
		return
	}
	explicitRevision := draft.Revision
	draft.sourceRevision = explicitRevision
	if draft.Revision == 0 {
		draft.Revision = 1
	}
	if draft.Sequence == 0 {
		draft.Sequence = uint64(b.retained + 1)
	}
	if draft.StreamID == "" {
		draft.StreamID = "provider:" + draft.SourceEventKey
	}
	if draft.Origin == "" {
		draft.Origin = lipsdkmetering.OriginProvider
	}
	if draft.Acquisition == "" {
		draft.Acquisition = lipsdkmetering.AcquisitionProviderResponse
	}
	if draft.Authority == "" {
		draft.Authority = lipsdkmetering.AuthorityObservedClaim
	}
	if draft.Perspective == "" {
		draft.Perspective = lipsdkmetering.PerspectiveOperator
	}
	if draft.Boundary == "" {
		draft.Boundary = lipsdkmetering.BoundaryBackendEgress
	}
	if draft.Lifecycle == "" {
		draft.Lifecycle = lipsdkmetering.LifecycleBackendAttempt
	}
	if draft.Semantics == "" {
		draft.Semantics = lipsdkmetering.SemanticsCumulative
	}
	if draft.Coverage == "" {
		draft.Coverage = "complete"
	}

	key := strings.TrimSpace(draft.SourceEventKey)
	draft.SourceEventKey = key
	hash := draftFingerprint(draft)
	if b.lastFingerprint == nil {
		b.lastFingerprint = make(map[string]string)
	}
	if b.lastRevision == nil {
		b.lastRevision = make(map[string]uint64)
	}
	if b.lastObservation == nil {
		b.lastObservation = make(map[string]lipsdkmetering.Observation)
	}
	// Replay suppression is deliberately consecutive. A provider can emit a
	// legitimate correction A -> B -> A; retaining every hash ever observed
	// would incorrectly discard the final A. Pending drafts are checked first,
	// then the last accepted drained payload supplies the cross-drain anchor.
	if explicitRevision != 0 {
		revisionKey := providerRevisionKey(key, explicitRevision)
		if _, exists := b.explicitRevisionFingerprint[revisionKey]; exists {
			// A source revision is immutable. A retry of its accepted payload is
			// idempotent; a changed payload at the same revision is rejected rather
			// than being mistaken for another correction with an unstable identity.
			return
		}
		for i := len(b.drafts) - 1; i >= 0; i-- {
			prior := b.drafts[i]
			if prior.SourceEventKey == key && prior.sourceRevision == explicitRevision {
				return
			}
		}
	} else {
		latest := b.lastFingerprint[key]
		for i := len(b.drafts) - 1; i >= 0; i-- {
			if b.drafts[i].SourceEventKey == key {
				latest = draftFingerprint(b.drafts[i])
				break
			}
		}
		if latest == hash {
			return
		}
	}
	if prior, exists := b.lastRevision[key]; exists && draft.Revision <= prior {
		draft.Revision = prior + 1
	}
	for _, prior := range b.drafts {
		if prior.SourceEventKey == key && prior.Revision >= draft.Revision {
			draft.Revision = prior.Revision + 1
		}
	}
	b.drafts = append(b.drafts, cloneProviderEvidenceDraft(draft))
	b.retained++
}

// AddUsageEvent maps the six legacy token counters and a genuine provider
// monetary field into a V2 draft. Family adapters call this after parsing
// their own wire schema; no provider-specific interpretation occurs here.
func (b *ProviderEvidenceBuffer) AddUsageEvent(ev lipapi.Event, mapping string) {
	if b == nil || ev.Kind != lipapi.EventUsageDelta {
		return
	}
	key := strings.TrimSpace(ev.Accounting.DedupeKey)
	if key == "" {
		key = strings.TrimSpace(mapping) + ":stream"
	}
	draft := ProviderEvidenceDraft{
		SourceEventKey:     key,
		StreamID:           strings.TrimSpace(mapping),
		Origin:             lipsdkmetering.OriginProvider,
		Acquisition:        lipsdkmetering.AcquisitionProviderResponse,
		Authority:          lipsdkmetering.AuthorityObservedClaim,
		Perspective:        lipsdkmetering.PerspectiveOperator,
		Boundary:           lipsdkmetering.BoundaryBackendEgress,
		Lifecycle:          lipsdkmetering.LifecycleBackendAttempt,
		Semantics:          lipsdkmetering.SemanticsCumulative,
		ProviderAccountKey: ev.Accounting.ProviderAccountKey,
		ProviderRequestID:  ev.Accounting.ProviderRequestID,
		ProviderChargeID:   ev.Accounting.ProviderChargeID,
		Measures:           providerTokenMeasures(ev, mapping),
		Evidence:           providerTokenEvidence(ev),
	}
	if ev.CostPresent && ev.Accounting.Source == lipapi.UsageSourceProviderReported && ev.CostNanoUnits >= 0 && strings.TrimSpace(ev.Currency) != "" {
		amount := lipsdkmetering.DecimalFromNanoUnits(ev.CostNanoUnits)
		draft.Charges = []lipsdkmetering.ReportedCharge{{
			ChargeItemID: "cost:" + key,
			Amount:       &amount,
			Currency:     strings.TrimSpace(ev.Currency),
			Kind:         lipsdkmetering.ChargeKindAggregate,
		}}
		draft.Evidence = append(draft.Evidence,
			lipsdkmetering.SafeEvidenceField{Path: "$.cost.amount", Lexeme: strconv.FormatInt(ev.CostNanoUnits, 10), Present: true, Acquisition: lipsdkmetering.AcquisitionProviderResponse},
			lipsdkmetering.SafeEvidenceField{Path: "$.cost.currency", Lexeme: strings.TrimSpace(ev.Currency), Present: true, Acquisition: lipsdkmetering.AcquisitionProviderResponse},
		)
	}
	if ev.Accounting.Source != "" {
		draft.Acquisition = providerAcquisition(ev.Accounting.Source)
	}
	if ev.Accounting.Authority != "" {
		draft.Authority = providerAuthority(ev.Accounting.Authority)
	}
	if strings.TrimSpace(ev.Accounting.ServiceContext) != "" {
		// Service context has no pricing meaning. Keep it as a bounded,
		// provider-owned lexeme under the safe evidence contract.
		draft.Evidence = append(draft.Evidence, lipsdkmetering.SafeEvidenceField{
			Path: "$.provider_schema.service_context", Lexeme: strings.TrimSpace(ev.Accounting.ServiceContext),
			Present: true, Acquisition: draft.Acquisition,
		})
	}
	b.Add(draft)
}

// BindEconomicEvidence supplies only trusted runtime lineage. Binding may be
// called once after Open; subsequent calls replace no already-drained data.
func (b *ProviderEvidenceBuffer) BindEconomicEvidence(identity ObservationIdentity) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.bound {
		b.mu.Unlock()
		return
	}
	b.identity = identity
	b.identity.StoreID = strings.TrimSpace(identity.StoreID)
	b.identity.RequestID = strings.TrimSpace(identity.RequestID)
	b.identity.CallID = strings.TrimSpace(identity.CallID)
	b.identity.BillingCallID = strings.TrimSpace(identity.BillingCallID)
	b.identity.ALegID = strings.TrimSpace(identity.ALegID)
	b.identity.BLegID = strings.TrimSpace(identity.BLegID)
	b.identity.AttemptID = strings.TrimSpace(identity.AttemptID)
	b.bound = true
	b.mu.Unlock()
}

// DrainEconomicObservations returns only valid, strictly B-leg-owned V2
// observations. An unbound stream returns no evidence rather than inventing a
// store or attaching provider quantities to a logical request.
func (b *ProviderEvidenceBuffer) DrainEconomicObservations() []lipsdkmetering.Observation {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.bound || b.identity.StoreID == "" || b.identity.BLegID == "" {
		return nil
	}
	identity := b.identity
	drafts := make([]ProviderEvidenceDraft, len(b.drafts))
	for i, draft := range b.drafts {
		drafts[i] = cloneProviderEvidenceDraft(draft)
	}
	b.drafts = nil

	observations := make([]lipsdkmetering.Observation, 0, len(drafts))
	if b.lastRevision == nil {
		b.lastRevision = make(map[string]uint64)
	}
	if b.lastObservation == nil {
		b.lastObservation = make(map[string]lipsdkmetering.Observation)
	}
	priorBySource := make(map[string]lipsdkmetering.Observation, len(b.lastObservation))
	for key, observation := range b.lastObservation {
		priorBySource[key] = observation.Clone()
	}
	for _, draft := range drafts {
		// Fingerprint the provider payload before adding host-owned supersession
		// metadata. Runtime timestamps, revisions and source relations must not
		// turn an unchanged provider snapshot into a new replay.
		payloadFingerprint := draftFingerprint(draft)
		// A changed payload is a new immutable observation, but it must retain
		// the source relation to the last accepted revision. Build the relation
		// only after the prior observation has passed validation so malformed
		// drafts cannot become supersession anchors.
		if len(draft.Supersedes) == 0 {
			if prior, exists := priorBySource[draft.SourceEventKey]; exists {
				if ref, err := prior.Ref(identity.StoreID); err == nil {
					draft.Supersedes = []lipsdkmetering.ObservationRef{ref}
					draft.Semantics = lipsdkmetering.SemanticsReplacement
				}
			}
		}
		if len(draft.Supersedes) != 0 && draft.Semantics != lipsdkmetering.SemanticsCorrection && draft.Semantics != lipsdkmetering.SemanticsReplacement {
			// A superseding provider snapshot replaces the prior source payload;
			// V2 intentionally does not permit a cumulative observation to carry
			// an immutable supersession edge.
			draft.Semantics = lipsdkmetering.SemanticsReplacement
		}
		if observation, err := providerObservationValidated(identity, draft); err == nil {
			priorBySource[draft.SourceEventKey] = observation
			b.lastRevision[draft.SourceEventKey] = draft.Revision
			b.lastObservation[draft.SourceEventKey] = observation.Clone()
			if payloadFingerprint != "" {
				b.rememberFingerprintLocked(draft.SourceEventKey, payloadFingerprint)
			}
			if sourceRevision := draft.sourceRevision; sourceRevision != 0 {
				b.rememberExplicitRevisionLocked(draft.SourceEventKey, sourceRevision, payloadFingerprint)
			}
			observations = append(observations, observation)
		}
	}
	return observations
}

func providerObservation(identity ObservationIdentity, draft ProviderEvidenceDraft) (lipsdkmetering.Observation, bool) {
	observation, err := providerObservationValidated(identity, draft)
	return observation, err == nil
}

func providerObservationValidated(identity ObservationIdentity, draft ProviderEvidenceDraft) (lipsdkmetering.Observation, error) {
	if strings.TrimSpace(identity.StoreID) == "" || strings.TrimSpace(identity.BLegID) == "" || strings.TrimSpace(draft.SourceEventKey) == "" {
		return lipsdkmetering.Observation{}, lipsdkmetering.ErrInvalidObservation
	}
	callID := strings.TrimSpace(identity.CallID)
	if callID == "" {
		callID = strings.TrimSpace(identity.BillingCallID)
	}
	billingCallID := strings.TrimSpace(identity.BillingCallID)
	if billingCallID == "" {
		billingCallID = callID
	}
	attemptID := strings.TrimSpace(identity.AttemptID)
	if attemptID == "" {
		attemptID = identity.BLegID
	}
	observedAt := draft.ObservedAt
	if observedAt.IsZero() {
		observedAt = identity.ObservedAt
	}
	if observedAt.IsZero() {
		observedAt = time.Unix(0, 0).UTC()
	}
	receivedAt := draft.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = identity.ReceivedAt
	}
	if receivedAt.IsZero() {
		receivedAt = observedAt
	}
	subject := lipsdkmetering.SubjectRef{
		Kind: lipsdkmetering.SubjectBLeg, StoreID: identity.StoreID,
		RequestID: identity.RequestID, CallID: callID, BillingCallID: billingCallID,
		ALegID: identity.ALegID, BLegID: identity.BLegID, AttemptID: attemptID,
		AttemptSeq: identity.AttemptSeq, ProviderAccountKey: draft.ProviderAccountKey,
		ProviderRequestID: draft.ProviderRequestID, ProviderChargeID: draft.ProviderChargeID,
	}
	correlation := lipsdkmetering.CorrelationV2{
		StoreID: identity.StoreID, RequestID: identity.RequestID, CallID: callID,
		BillingCallID: billingCallID, ALegID: identity.ALegID, BLegID: identity.BLegID,
		AttemptID: attemptID, AttemptSeq: identity.AttemptSeq,
		ProviderAccountKey: draft.ProviderAccountKey, ProviderRequestID: draft.ProviderRequestID,
		ProviderChargeID: draft.ProviderChargeID,
	}
	seed := strings.Join([]string{identity.StoreID, billingCallID, identity.BLegID, draft.StreamID, draft.SourceEventKey, strconv.FormatUint(draft.Revision, 10)}, "\x00")
	hash := sha256.Sum256([]byte(seed))
	observation := lipsdkmetering.Observation{
		Version:        lipsdkmetering.ObservationVersionV2,
		ID:             "lip-provider-" + hex.EncodeToString(hash[:]),
		SourceEventKey: draft.SourceEventKey, Revision: draft.Revision,
		StreamID: draft.StreamID, Sequence: draft.Sequence,
		Origin: draft.Origin, Acquisition: draft.Acquisition, Authority: draft.Authority,
		Perspective: draft.Perspective, Boundary: draft.Boundary, Lifecycle: draft.Lifecycle,
		Subject: subject, Correlation: correlation, Scope: identity.Scope.Clone(),
		Semantics: draft.Semantics, ObservedAt: observedAt.UTC(), ReceivedAt: receivedAt.UTC(),
		MappingRef: draft.StreamID, Measures: cloneMeasures(draft.Measures),
		Charges: cloneCharges(draft.Charges), Supersedes: append([]lipsdkmetering.ObservationRef(nil), draft.Supersedes...),
		Evidence: append([]lipsdkmetering.SafeEvidenceField(nil), draft.Evidence...),
	}
	if err := observation.Validate(); err != nil {
		return lipsdkmetering.Observation{}, err
	}
	return observation, nil
}

func ProviderUsageEvent(ev lipapi.Event, mapping, sourceKey string) ProviderEvidenceDraft {
	if strings.TrimSpace(sourceKey) == "" {
		sourceKey = strings.TrimSpace(ev.Accounting.DedupeKey)
	}
	if strings.TrimSpace(sourceKey) == "" {
		sourceKey = strings.TrimSpace(mapping) + ":stream"
	}
	draft := ProviderEvidenceDraft{
		SourceEventKey: sourceKey, StreamID: mapping, Measures: providerTokenMeasures(ev, mapping),
		Evidence: providerTokenEvidence(ev), ProviderAccountKey: ev.Accounting.ProviderAccountKey,
		ProviderRequestID: ev.Accounting.ProviderRequestID, ProviderChargeID: ev.Accounting.ProviderChargeID,
	}
	if ev.Accounting.Source != "" {
		draft.Acquisition = providerAcquisition(ev.Accounting.Source)
	}
	if ev.Accounting.Authority != "" {
		draft.Authority = providerAuthority(ev.Accounting.Authority)
	}
	if strings.TrimSpace(ev.Accounting.ServiceContext) != "" {
		draft.Evidence = append(draft.Evidence, lipsdkmetering.SafeEvidenceField{
			Path: "$.provider_schema.service_context", Lexeme: strings.TrimSpace(ev.Accounting.ServiceContext),
			Present: true, Acquisition: draft.Acquisition,
		})
	}
	return draft
}

func providerTokenMeasures(ev lipapi.Event, mapping string) []lipsdkmetering.Measure {
	quality := lipsdkmetering.QualityObserved
	method := strings.TrimSpace(mapping)
	if method == "" {
		method = "provider.usage.v2"
	}
	measures := make([]lipsdkmetering.Measure, 0, 6)
	appendToken := func(component string, direction lipsdkmetering.FlowDirection, value int, present bool) {
		if !present || value < 0 {
			return
		}
		decimal := lipsdkmetering.Decimal{Coefficient: strconv.Itoa(value)}
		measures = append(measures, lipsdkmetering.Measure{Key: lipsdkmetering.ComponentKey{Direction: direction, Component: component, Unit: lipsdkmetering.UnitToken, SchemaID: lipsdkmetering.DefaultInclusionSchemaID}, Value: &decimal, Quality: quality, MethodRef: method})
	}
	appendToken(lipsdkmetering.ComponentInputToken, lipsdkmetering.DirectionInput, ev.InputTokens, ev.UsagePresence.InputTokens)
	appendToken(lipsdkmetering.ComponentOutputToken, lipsdkmetering.DirectionOutput, ev.OutputTokens, ev.UsagePresence.OutputTokens)
	appendToken(lipsdkmetering.ComponentCacheReadInputToken, lipsdkmetering.DirectionInput, ev.CacheReadTokens, ev.UsagePresence.CacheReadTokens)
	appendToken(lipsdkmetering.ComponentCacheWriteInputToken, lipsdkmetering.DirectionInput, ev.CacheWriteTokens, ev.UsagePresence.CacheWriteTokens)
	appendToken(lipsdkmetering.ComponentReasoningOutputToken, lipsdkmetering.DirectionOutput, ev.ReasoningTokens, ev.UsagePresence.ReasoningTokens)
	appendToken(lipsdkmetering.ComponentTotalToken, lipsdkmetering.DirectionNone, ev.TotalTokens, ev.UsagePresence.TotalTokens)
	return measures
}

func providerTokenEvidence(ev lipapi.Event) []lipsdkmetering.SafeEvidenceField {
	fields := make([]lipsdkmetering.SafeEvidenceField, 0, 6)
	appendField := func(path string, value int, present bool) {
		if !present || value < 0 {
			return
		}
		fields = append(fields, lipsdkmetering.SafeEvidenceField{Path: path, Lexeme: strconv.Itoa(value), Present: true, Acquisition: lipsdkmetering.AcquisitionProviderResponse})
	}
	appendField("$.usage.input_tokens", ev.InputTokens, ev.UsagePresence.InputTokens)
	appendField("$.usage.output_tokens", ev.OutputTokens, ev.UsagePresence.OutputTokens)
	appendField("$.usage.cache_read_input_tokens", ev.CacheReadTokens, ev.UsagePresence.CacheReadTokens)
	appendField("$.usage.cache_write_input_tokens", ev.CacheWriteTokens, ev.UsagePresence.CacheWriteTokens)
	appendField("$.usage.reasoning_output_tokens", ev.ReasoningTokens, ev.UsagePresence.ReasoningTokens)
	appendField("$.usage.total_tokens", ev.TotalTokens, ev.UsagePresence.TotalTokens)
	return fields
}

func providerAcquisition(source lipapi.UsageSource) string {
	if source == lipapi.UsageSourceProviderCountAPI {
		return lipsdkmetering.AcquisitionProviderCountAPI
	}
	return lipsdkmetering.AcquisitionProviderResponse
}

func providerAuthority(authority lipapi.UsageAuthority) string {
	switch authority {
	case lipapi.UsageAuthorityEstimated, lipapi.UsageAuthorityAdvisory:
		return lipsdkmetering.AuthorityEstimatedClaim
	case lipapi.UsageAuthorityUnavailable:
		return lipsdkmetering.AuthorityUnavailableClaim
	default:
		return lipsdkmetering.AuthorityObservedClaim
	}
}

func draftFingerprint(draft ProviderEvidenceDraft) string {
	data, err := json.Marshal(struct {
		Key                string
		Stream             string
		Origin             string
		Acquisition        string
		Authority          string
		Perspective        lipsdkmetering.EconomicPerspective
		Boundary           lipsdkmetering.Boundary
		Lifecycle          lipsdkmetering.LifecycleScope
		Semantics          string
		ProviderAccountKey string
		ProviderRequestID  string
		ProviderChargeID   string
		Measures           []lipsdkmetering.Measure
		Charges            []lipsdkmetering.ReportedCharge
		Supersedes         []lipsdkmetering.ObservationRef
		Evidence           []lipsdkmetering.SafeEvidenceField
		Coverage           string
		CoverageReason     string
	}{
		draft.SourceEventKey, draft.StreamID, draft.Origin, draft.Acquisition,
		draft.Authority, draft.Perspective, draft.Boundary, draft.Lifecycle,
		draft.Semantics, draft.ProviderAccountKey, draft.ProviderRequestID,
		draft.ProviderChargeID, draft.Measures, draft.Charges, draft.Supersedes,
		draft.Evidence, draft.Coverage, draft.CoverageReason,
	})
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func (b *ProviderEvidenceBuffer) rememberFingerprintLocked(key, fingerprint string) {
	if b == nil || key == "" || fingerprint == "" {
		return
	}
	if b.lastFingerprint == nil {
		b.lastFingerprint = make(map[string]string)
	}
	if _, exists := b.lastFingerprint[key]; !exists {
		if len(b.lastFingerprint) >= maxProviderEvidenceDrafts && len(b.fingerprintOrder) > 0 {
			oldest := b.fingerprintOrder[0]
			b.fingerprintOrder = b.fingerprintOrder[1:]
			delete(b.lastFingerprint, oldest)
		}
		b.fingerprintOrder = append(b.fingerprintOrder, key)
	}
	b.lastFingerprint[key] = fingerprint
}

func providerRevisionKey(key string, revision uint64) string {
	return key + "\x00" + strconv.FormatUint(revision, 10)
}

func (b *ProviderEvidenceBuffer) rememberExplicitRevisionLocked(key string, revision uint64, fingerprint string) {
	if b == nil || key == "" || revision == 0 || fingerprint == "" {
		return
	}
	if b.explicitRevisionFingerprint == nil {
		b.explicitRevisionFingerprint = make(map[string]string)
	}
	revisionKey := providerRevisionKey(key, revision)
	if _, exists := b.explicitRevisionFingerprint[revisionKey]; !exists {
		if len(b.explicitRevisionFingerprint) >= maxProviderEvidenceDrafts && len(b.explicitRevisionOrder) > 0 {
			oldest := b.explicitRevisionOrder[0]
			b.explicitRevisionOrder = b.explicitRevisionOrder[1:]
			delete(b.explicitRevisionFingerprint, oldest)
		}
		b.explicitRevisionOrder = append(b.explicitRevisionOrder, revisionKey)
	}
	// Preserve the first accepted payload for this source revision. Add rejects
	// every later payload at the same source revision, whether identical or
	// conflicting, so the first hash remains an idempotent replay anchor.
	if _, exists := b.explicitRevisionFingerprint[revisionKey]; !exists {
		b.explicitRevisionFingerprint[revisionKey] = fingerprint
	}
}

func cloneProviderEvidenceDraft(in ProviderEvidenceDraft) ProviderEvidenceDraft {
	out := in
	out.Measures = cloneMeasures(in.Measures)
	out.Charges = cloneCharges(in.Charges)
	out.Supersedes = append([]lipsdkmetering.ObservationRef(nil), in.Supersedes...)
	out.Evidence = append([]lipsdkmetering.SafeEvidenceField(nil), in.Evidence...)
	return out
}

func cloneMeasures(in []lipsdkmetering.Measure) []lipsdkmetering.Measure {
	if in == nil {
		return nil
	}
	out := make([]lipsdkmetering.Measure, len(in))
	for i := range in {
		out[i] = in[i].Clone()
	}
	return out
}

func cloneCharges(in []lipsdkmetering.ReportedCharge) []lipsdkmetering.ReportedCharge {
	if in == nil {
		return nil
	}
	out := make([]lipsdkmetering.ReportedCharge, len(in))
	for i := range in {
		out[i] = in[i].Clone()
	}
	return out
}
