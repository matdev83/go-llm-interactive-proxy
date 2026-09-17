package billing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/replay"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// EconomicQueue identifies the independently recoverable customer and
// provider economic queues. A worker is bound to one queue and therefore
// cannot accidentally process work owned by the other economic party.
type EconomicQueue string

const (
	EconomicQueueCustomer EconomicQueue = "customer"
	EconomicQueueProvider EconomicQueue = "provider"
)

func (q EconomicQueue) String() string { return string(q) }

func (q EconomicQueue) Validate() error {
	switch q {
	case EconomicQueueCustomer, EconomicQueueProvider:
		return nil
	default:
		return fmt.Errorf("%w: unknown queue %q", ErrInvalidEconomicRevision, q)
	}
}

var (
	// ErrInvalidEconomicRevision identifies malformed durable revision work.
	ErrInvalidEconomicRevision = errors.New("billing: invalid economic revision")
	// ErrEconomicRevisionSubjectMismatch prevents a rater/reconciler from
	// writing a result for a different trusted B-leg or economic subject.
	ErrEconomicRevisionSubjectMismatch = errors.New("billing: economic revision subject mismatch")
	// ErrEconomicRevisionBasisMismatch prevents a derived result from silently
	// changing the explicit plane selected by durable work.
	ErrEconomicRevisionBasisMismatch = errors.New("billing: economic revision basis mismatch")
	// ErrEconomicRevisionInputMismatch identifies a result whose immutable input
	// references no longer describe the revision that caused the work.
	ErrEconomicRevisionInputMismatch = errors.New("billing: economic revision input mismatch")
	// ErrEconomicRevisionConflict identifies a same-identity result with changed
	// derived output. Durable stores must retain the first result and reject the
	// conflicting replay rather than overwrite it.
	ErrEconomicRevisionConflict = errors.New("billing: economic revision conflict")
	// ErrEconomicRevisionClaimLost identifies a stale worker lease attempting
	// to retire or retry work after another worker fenced it out.
	ErrEconomicRevisionClaimLost = errors.New("billing: economic revision claim lost")
	// ErrEconomicRevisionFence identifies an unsafe current-head transition,
	// including a same-revision evidence branch that cannot be proven to contain
	// or be contained by the durable head evidence.
	ErrEconomicRevisionFence = errors.New("billing: economic revision head fence conflict")
)

// EconomicRevisionInputMismatchError reports a valuation whose input identity
// does not match the canonical identity expected at the worker boundary.
// It unwraps to ErrEconomicRevisionInputMismatch so callers can retain the
// existing sentinel classification while using errors.As for the hashes.
type EconomicRevisionInputMismatchError struct {
	Expected string
	Actual   string
}

func (e *EconomicRevisionInputMismatchError) Error() string {
	if e == nil {
		return ErrEconomicRevisionInputMismatch.Error()
	}
	return fmt.Sprintf("%s: expected=%q actual=%q", ErrEconomicRevisionInputMismatch, e.Expected, e.Actual)
}

func (e *EconomicRevisionInputMismatchError) Unwrap() error {
	return ErrEconomicRevisionInputMismatch
}

// EconomicRevisionIdentity is the stable identity of one pure valuation
// attempt. The key deliberately includes queue, head, evidence revision and
// input-set hash, so corrected evidence is a new immutable work item.
type EconomicRevisionIdentity struct {
	Queue            EconomicQueue
	HeadKey          string
	EvidenceRevision uint64
	InputSetHash     string
}

// EconomicEvidenceSetRelation describes the candidate evidence set relative
// to the current durable head. The relation is based on canonical immutable
// observation references, never on the SHA-256 input-set hash ordering.
type EconomicEvidenceSetRelation uint8

const (
	EconomicEvidenceSetEqual EconomicEvidenceSetRelation = iota
	EconomicEvidenceSetCandidateSuperset
	EconomicEvidenceSetCandidateSubset
	EconomicEvidenceSetIncomparable
)

// CompareEconomicEvidenceSets compares a candidate's canonical observation
// references with the references retained by the current durable head. Exact
// duplicate references are collapsed. Two payload hashes for one
// store/observation/revision identity are rejected because they cannot both be
// members of one immutable evidence set.
func CompareEconomicEvidenceSets(current, candidate []metering.ObservationRef) (EconomicEvidenceSetRelation, error) {
	current, err := canonicalEconomicEvidenceRefs(current)
	if err != nil {
		return EconomicEvidenceSetIncomparable, err
	}
	candidate, err = canonicalEconomicEvidenceRefs(candidate)
	if err != nil {
		return EconomicEvidenceSetIncomparable, err
	}
	if len(current) == len(candidate) {
		equal := true
		for i := range current {
			if !current[i].Equal(candidate[i]) {
				equal = false
				break
			}
		}
		if equal {
			return EconomicEvidenceSetEqual, nil
		}
	}
	currentRefs := make(map[metering.ObservationRef]struct{}, len(current))
	for _, ref := range current {
		currentRefs[ref] = struct{}{}
	}
	candidateRefs := make(map[metering.ObservationRef]struct{}, len(candidate))
	for _, ref := range candidate {
		candidateRefs[ref] = struct{}{}
	}
	currentSubsetCandidate := true
	for ref := range currentRefs {
		if _, ok := candidateRefs[ref]; !ok {
			currentSubsetCandidate = false
			break
		}
	}
	candidateSubsetCurrent := true
	for ref := range candidateRefs {
		if _, ok := currentRefs[ref]; !ok {
			candidateSubsetCurrent = false
			break
		}
	}
	switch {
	case currentSubsetCandidate:
		return EconomicEvidenceSetCandidateSuperset, nil
	case candidateSubsetCurrent:
		return EconomicEvidenceSetCandidateSubset, nil
	default:
		return EconomicEvidenceSetIncomparable, nil
	}
}

func canonicalEconomicEvidenceRefs(refs []metering.ObservationRef) ([]metering.ObservationRef, error) {
	ordered := append([]metering.ObservationRef(nil), refs...)
	for i, ref := range ordered {
		if err := ref.Validate(); err != nil {
			return nil, fmt.Errorf("%w: evidence reference %d: %v", ErrInvalidEconomicRevision, i, err)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]
		if left.StoreID != right.StoreID {
			return left.StoreID < right.StoreID
		}
		if left.ObservationID != right.ObservationID {
			return left.ObservationID < right.ObservationID
		}
		if left.Revision != right.Revision {
			return left.Revision < right.Revision
		}
		return left.PayloadHash < right.PayloadHash
	})
	canonical := ordered[:0]
	type revisionIdentity struct {
		storeID, observationID string
		revision               uint64
	}
	seen := make(map[revisionIdentity]string, len(ordered))
	for _, ref := range ordered {
		identity := revisionIdentity{storeID: ref.StoreID, observationID: ref.ObservationID, revision: ref.Revision}
		if prior, exists := seen[identity]; exists {
			if prior != ref.PayloadHash {
				return nil, fmt.Errorf("%w: conflicting payload hashes for %s/%s revision %d", ErrInvalidEconomicRevision, ref.StoreID, ref.ObservationID, ref.Revision)
			}
			continue
		}
		seen[identity] = ref.PayloadHash
		canonical = append(canonical, ref)
	}
	return canonical, nil
}

func (i EconomicRevisionIdentity) replayIdentity() (replay.RevisionIdentity, error) {
	return replay.NewRevisionIdentity(i.Queue.String(), i.HeadKey, i.EvidenceRevision, i.InputSetHash)
}

func (i EconomicRevisionIdentity) validate() error {
	if err := i.Queue.Validate(); err != nil {
		return err
	}
	if err := economics.ValidateSafeRef("economic revision head key", i.HeadKey); err != nil || strings.TrimSpace(i.HeadKey) != i.HeadKey {
		if err == nil {
			err = errors.New("head key must not have surrounding whitespace")
		}
		return fmt.Errorf("%w: head key: %v", ErrInvalidEconomicRevision, err)
	}
	if i.EvidenceRevision == 0 {
		return fmt.Errorf("%w: evidence revision is required", ErrInvalidEconomicRevision)
	}
	if _, err := i.replayIdentity(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEconomicRevision, err)
	}
	return nil
}

// Validate checks that every member required to derive a durable revision key
// is present and canonical.
func (i EconomicRevisionIdentity) Validate() error { return i.validate() }

// Key returns a bounded opaque identity suitable for durable work IDs.
func (i EconomicRevisionIdentity) Key() string {
	if err := i.validate(); err != nil {
		return ""
	}
	replayIdentity, _ := i.replayIdentity()
	return replayIdentity.Key()
}

// ValuationKey returns the immutable valuation ID derived from this revision.
func (i EconomicRevisionIdentity) ValuationKey() string {
	key := i.Key()
	if key == "" {
		return ""
	}
	digest := sha256.Sum256([]byte("valuation\x00" + key))
	return "economic-valuation:v1:" + hex.EncodeToString(digest[:])
}

// ReconciliationKey returns the immutable reconciliation ID derived from this
// revision. Reconciliation and valuation identities remain distinct records,
// while both are tied to the same work identity.
func (i EconomicRevisionIdentity) ReconciliationKey() string {
	key := i.Key()
	if key == "" {
		return ""
	}
	digest := sha256.Sum256([]byte("reconciliation\x00" + key))
	return "economic-reconciliation:v1:" + hex.EncodeToString(digest[:])
}

// Less provides the deterministic identity ordering used when evidence sets
// are equal or cannot be compared. Durable head stores must compare their
// retained observation references first so this lexical fallback can never
// discard a strict evidence superset at the same revision.
func (i EconomicRevisionIdentity) Less(other EconomicRevisionIdentity) bool {
	if i.Queue != other.Queue {
		return i.Queue < other.Queue
	}
	if i.HeadKey != other.HeadKey {
		return i.HeadKey < other.HeadKey
	}
	if i.EvidenceRevision != other.EvidenceRevision {
		return i.EvidenceRevision < other.EvidenceRevision
	}
	if i.InputSetHash != other.InputSetHash {
		return i.InputSetHash < other.InputSetHash
	}
	return i.Key() < other.Key()
}

// EconomicRevisionWork is the durable, immutable input to pure economic
// computation. Input contains only provider-neutral immutable observations;
// no account balance or journal operation is present in this envelope.
type EconomicRevisionWork struct {
	Queue            EconomicQueue                  `json:"queue"`
	HeadKey          string                         `json:"head_key"`
	Subject          metering.SubjectRef            `json:"subject"`
	EvidenceRevision uint64                         `json:"evidence_revision"`
	InputSetHash     string                         `json:"input_set_hash"`
	Input            economics.PostUsageRatingInput `json:"input"`
	CreatedAt        time.Time                      `json:"created_at"`
}

// Normalize validates the durable work and fills the canonical input-set
// hash. It returns a deep copy so callers cannot mutate queued work through
// shared observation slices.
func (w EconomicRevisionWork) Normalize() (EconomicRevisionWork, error) {
	out := w
	if err := out.Queue.Validate(); err != nil {
		return EconomicRevisionWork{}, err
	}
	if err := economics.ValidateSafeRef("economic revision head key", out.HeadKey); err != nil || strings.TrimSpace(out.HeadKey) != out.HeadKey {
		if err == nil {
			err = errors.New("head key must not have surrounding whitespace")
		}
		return EconomicRevisionWork{}, fmt.Errorf("%w: head key: %v", ErrInvalidEconomicRevision, err)
	}
	if out.EvidenceRevision == 0 {
		return EconomicRevisionWork{}, fmt.Errorf("%w: evidence revision is required", ErrInvalidEconomicRevision)
	}
	if err := out.Subject.Validate(); err != nil {
		return EconomicRevisionWork{}, fmt.Errorf("%w: subject: %v", ErrInvalidEconomicRevision, err)
	}
	out.Input = out.Input.Clone()
	if !sameSubject(out.Subject, out.Input.Subject) {
		return EconomicRevisionWork{}, fmt.Errorf("%w: work subject and rating subject differ", ErrEconomicRevisionSubjectMismatch)
	}
	if err := out.Input.Validate(); err != nil {
		return EconomicRevisionWork{}, fmt.Errorf("%w: rating input: %v", ErrInvalidEconomicRevision, err)
	}
	refs, err := ratingInputObservationRefs(out.Input)
	if err != nil {
		return EconomicRevisionWork{}, fmt.Errorf("%w: observation refs: %v", ErrInvalidEconomicRevision, err)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].StoreID != refs[j].StoreID {
			return refs[i].StoreID < refs[j].StoreID
		}
		if refs[i].ObservationID != refs[j].ObservationID {
			return refs[i].ObservationID < refs[j].ObservationID
		}
		if refs[i].Revision != refs[j].Revision {
			return refs[i].Revision < refs[j].Revision
		}
		return refs[i].PayloadHash < refs[j].PayloadHash
	})
	computedHash, err := economics.CanonicalInputSetHash(out.Input.Basis, refs)
	if err != nil {
		return EconomicRevisionWork{}, fmt.Errorf("%w: input-set hash: %v", ErrInvalidEconomicRevision, err)
	}
	if out.InputSetHash != "" && out.InputSetHash != computedHash {
		return EconomicRevisionWork{}, fmt.Errorf("%w: supplied=%q expected=%q", economics.ErrInputSetHashMismatch, out.InputSetHash, computedHash)
	}
	if out.Input.InputSetHash != "" && out.Input.InputSetHash != computedHash {
		return EconomicRevisionWork{}, fmt.Errorf("%w: input supplied=%q expected=%q", economics.ErrInputSetHashMismatch, out.Input.InputSetHash, computedHash)
	}
	out.InputSetHash = computedHash
	out.Input.InputSetHash = computedHash
	out.Input.ObservationRefs = append([]metering.ObservationRef(nil), refs...)
	if out.CreatedAt.IsZero() {
		// A zero timestamp in a synthetic/replayed envelope must not make the
		// derived result change on every restart. Durable adapters may set their
		// append timestamp separately; the payload remains deterministic.
		out.CreatedAt = time.Unix(0, 0).UTC()
	} else {
		out.CreatedAt = out.CreatedAt.UTC()
	}
	return out, nil
}

func (w EconomicRevisionWork) Identity() (EconomicRevisionIdentity, error) {
	normalized, err := w.Normalize()
	if err != nil {
		return EconomicRevisionIdentity{}, err
	}
	identity := EconomicRevisionIdentity{Queue: normalized.Queue, HeadKey: normalized.HeadKey, EvidenceRevision: normalized.EvidenceRevision, InputSetHash: normalized.InputSetHash}
	if _, err := identity.replayIdentity(); err != nil {
		return EconomicRevisionIdentity{}, fmt.Errorf("%w: %v", ErrInvalidEconomicRevision, err)
	}
	return identity, nil
}

func ratingInputObservationRefs(input economics.PostUsageRatingInput) ([]metering.ObservationRef, error) {
	if len(input.Observations) == 0 {
		return append([]metering.ObservationRef(nil), input.ObservationRefs...), nil
	}
	refs := make([]metering.ObservationRef, 0, len(input.Observations))
	for i, observation := range input.Observations {
		ref, err := observation.Ref(input.Subject.StoreID)
		if err != nil {
			return nil, fmt.Errorf("observation %d: %w", i, err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func sameSubject(left, right metering.SubjectRef) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

// EconomicReconciliation is the pure reconciliation result associated with a
// revision. It intentionally mirrors the durable reconciliation envelope but
// remains in the billing domain so the worker has no infrastructure dependency.
type EconomicReconciliation struct {
	ID                string
	Version           uint64
	Subject           metering.SubjectRef
	Scope             string
	Basis             economics.ValuationBasis
	InputSetHash      string
	LocalInputHash    string
	ProviderInputHash string
	PolicyID          string
	PolicyVersion     string
	ResultJSON        json.RawMessage
	CreatedAt         time.Time
}

func (r EconomicReconciliation) Normalize() (EconomicReconciliation, error) {
	out := r
	if err := economics.ValidateSafeRef("reconciliation id", out.ID); err != nil || strings.TrimSpace(out.ID) != out.ID {
		if err == nil {
			err = errors.New("id must not have surrounding whitespace")
		}
		return EconomicReconciliation{}, fmt.Errorf("%w: reconciliation id: %v", ErrInvalidEconomicRevision, err)
	}
	if out.Version == 0 {
		return EconomicReconciliation{}, fmt.Errorf("%w: reconciliation version is required", ErrInvalidEconomicRevision)
	}
	if err := out.Subject.Validate(); err != nil {
		return EconomicReconciliation{}, fmt.Errorf("%w: reconciliation subject: %v", ErrInvalidEconomicRevision, err)
	}
	if err := out.Basis.Validate(); err != nil {
		return EconomicReconciliation{}, fmt.Errorf("%w: reconciliation basis: %v", ErrInvalidEconomicRevision, err)
	}
	for name, hash := range map[string]string{
		"input_set_hash": out.InputSetHash, "local_input_hash": out.LocalInputHash, "provider_input_hash": out.ProviderInputHash,
	} {
		if hash == "" {
			continue
		}
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != sha256.Size || strings.ToLower(hash) != hash {
			return EconomicReconciliation{}, fmt.Errorf("%w: %s must be lowercase SHA-256 hex", ErrInvalidEconomicRevision, name)
		}
	}
	if !json.Valid(out.ResultJSON) {
		return EconomicReconciliation{}, fmt.Errorf("%w: reconciliation result JSON is invalid", ErrInvalidEconomicRevision)
	}
	var decoded any
	if err := json.Unmarshal(out.ResultJSON, &decoded); err != nil {
		return EconomicReconciliation{}, fmt.Errorf("%w: reconciliation result JSON: %v", ErrInvalidEconomicRevision, err)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return EconomicReconciliation{}, fmt.Errorf("%w: reconciliation canonical JSON: %v", ErrInvalidEconomicRevision, err)
	}
	out.ResultJSON = canonical
	if out.CreatedAt.IsZero() {
		out.CreatedAt = time.Unix(0, 0).UTC()
	} else {
		out.CreatedAt = out.CreatedAt.UTC()
	}
	return out, nil
}

// EconomicRevisionResult contains only pure derived records. Persisting it
// must never call a balance, exposure, settlement or journal mutation API.
type EconomicRevisionResult struct {
	Valuation      economics.Valuation     `json:"valuation"`
	Reconciliation *EconomicReconciliation `json:"reconciliation,omitempty"`
}

// EconomicValuationHead is the rebuildable current pointer for one queue/head.
// Immutable valuation and reconciliation history remains authoritative.
type EconomicValuationHead struct {
	Queue                 EconomicQueue
	HeadKey               string
	Subject               metering.SubjectRef
	EvidenceRevision      uint64
	InputSetHash          string
	WorkID                string
	ValuationID           string
	ValuationVersion      uint32
	ReconciliationID      string
	ReconciliationVersion uint64
	Fingerprint           string
	HeadVersion           uint64
	Fence                 uint64
	UpdatedAt             time.Time
}

// IsOlderThan reports whether a candidate revision should replace this head.
// A malformed existing head is treated as older so recovery can repair it.
func (h EconomicValuationHead) IsOlderThan(identity EconomicRevisionIdentity) bool {
	existing, err := NewEconomicRevisionIdentity(h.Queue, h.HeadKey, h.EvidenceRevision, h.InputSetHash)
	if err != nil {
		return true
	}
	return existing.Less(identity)
}

// NewEconomicRevisionIdentity validates and constructs the domain identity.
func NewEconomicRevisionIdentity(queue EconomicQueue, headKey string, revision uint64, inputHash string) (EconomicRevisionIdentity, error) {
	identity := EconomicRevisionIdentity{Queue: queue, HeadKey: headKey, EvidenceRevision: revision, InputSetHash: inputHash}
	if err := identity.validate(); err != nil {
		return EconomicRevisionIdentity{}, err
	}
	return identity, nil
}

// EconomicRevisionWorkReader supplies immutable revisions from durable queue
// state. Implementations must filter by queue before returning work.
type EconomicRevisionWorkReader interface {
	ListPendingEconomicRevisionWork(context.Context, EconomicQueue, int) ([]EconomicRevisionWork, error)
}

// EconomicRevisionWorkClaim is the fencing token for one bounded pure-work
// attempt. A finite lease lets another worker recover abandoned processing;
// Fence prevents the old owner from retiring the recovered work.
type EconomicRevisionWorkClaim struct {
	Owner      string
	Fence      uint64
	LeaseUntil time.Time
}

// EconomicRevisionWorkStateStore owns mutable delivery state separately from
// immutable evidence markers. Implementations must compare Owner and Fence
// when completing or retrying a claim so a stale worker cannot overwrite a
// newer worker's progress.
type EconomicRevisionWorkStateStore interface {
	ClaimEconomicRevisionWork(context.Context, EconomicRevisionWork, string, time.Duration) (EconomicRevisionWorkClaim, bool, error)
	CompleteEconomicRevisionWork(context.Context, EconomicRevisionWork, EconomicRevisionWorkClaim) error
	RetryEconomicRevisionWork(context.Context, EconomicRevisionWork, EconomicRevisionWorkClaim, string, time.Time) error
}

// EconomicRevisionResultStore persists pure results and advances the
// rebuildable current head atomically. It must not mutate monetary balances.
type EconomicRevisionResultStore interface {
	AppendEconomicRevisionResult(context.Context, EconomicRevisionWork, EconomicRevisionResult) error
}

// EconomicRevisionResultProbe is optional. Durable implementations use it to
// avoid invoking an expensive rater again after restart/replay.
type EconomicRevisionResultProbe interface {
	HasEconomicRevisionResult(context.Context, EconomicRevisionIdentity) (bool, error)
}

// EconomicRevisionReconciler computes a pure reconciliation envelope. It is
// optional; valuation remains independently durable when no reconciler exists.
type EconomicRevisionReconciler interface {
	Reconcile(context.Context, EconomicRevisionWork, economics.Valuation) (*EconomicReconciliation, error)
}
