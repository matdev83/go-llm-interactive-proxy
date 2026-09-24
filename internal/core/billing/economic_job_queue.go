package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// EconomicWorkKind identifies the pure computation a durable economic job
// performs. Rating kinds map one-to-one onto the customer/provider queues;
// separated reconciliation work is supplier-side computation that may depend
// on the immutable rating outputs of either queue without holding a customer
// balance lock.
type EconomicWorkKind string

const (
	EconomicWorkKindCustomerRating EconomicWorkKind = "customer_rating"
	EconomicWorkKindProviderRating EconomicWorkKind = "provider_rating"
	EconomicWorkKindReconciliation EconomicWorkKind = "reconciliation"
)

// AllEconomicWorkKinds returns every documented work kind in stable order.
func AllEconomicWorkKinds() []EconomicWorkKind {
	return []EconomicWorkKind{
		EconomicWorkKindCustomerRating,
		EconomicWorkKindProviderRating,
		EconomicWorkKindReconciliation,
	}
}

func (k EconomicWorkKind) String() string { return string(k) }

func (k EconomicWorkKind) Validate() error {
	switch k {
	case EconomicWorkKindCustomerRating, EconomicWorkKindProviderRating, EconomicWorkKindReconciliation:
		return nil
	default:
		return fmt.Errorf("%w: unknown economic work kind %q", ErrInvalidEconomicRevision, k)
	}
}

// IsRating reports whether the kind computes a rating valuation output that
// other work may depend on.
func (k EconomicWorkKind) IsRating() bool {
	return k == EconomicWorkKindCustomerRating || k == EconomicWorkKindProviderRating
}

// Queue returns the single economic queue that owns the kind. Customer and
// provider rating remain strictly separated; separated reconciliation work is
// supplier computation and therefore belongs to the provider queue.
func (k EconomicWorkKind) Queue() (EconomicQueue, error) {
	switch k {
	case EconomicWorkKindCustomerRating:
		return EconomicQueueCustomer, nil
	case EconomicWorkKindProviderRating, EconomicWorkKindReconciliation:
		return EconomicQueueProvider, nil
	default:
		return "", fmt.Errorf("%w: unknown economic work kind %q", ErrInvalidEconomicRevision, k)
	}
}

// EconomicWorkKindForQueue returns the rating kind derived from a queue.
func EconomicWorkKindForQueue(queue EconomicQueue) EconomicWorkKind {
	if queue == EconomicQueueCustomer {
		return EconomicWorkKindCustomerRating
	}
	return EconomicWorkKindProviderRating
}

// EconomicWorkStatus is the closed delivery-state vocabulary for durable
// economic job queue state. Completed and failed are terminal.
type EconomicWorkStatus string

const (
	EconomicWorkStatusPending    EconomicWorkStatus = "pending"
	EconomicWorkStatusProcessing EconomicWorkStatus = "processing"
	EconomicWorkStatusCompleted  EconomicWorkStatus = "completed"
	EconomicWorkStatusFailed     EconomicWorkStatus = "failed"
)

// AllEconomicWorkStatuses returns every documented work status in stable order.
func AllEconomicWorkStatuses() []EconomicWorkStatus {
	return []EconomicWorkStatus{
		EconomicWorkStatusPending,
		EconomicWorkStatusProcessing,
		EconomicWorkStatusCompleted,
		EconomicWorkStatusFailed,
	}
}

func (s EconomicWorkStatus) String() string { return string(s) }

func (s EconomicWorkStatus) Validate() error {
	switch s {
	case EconomicWorkStatusPending, EconomicWorkStatusProcessing, EconomicWorkStatusCompleted, EconomicWorkStatusFailed:
		return nil
	default:
		return fmt.Errorf("%w: unknown economic work status %q", ErrInvalidEconomicRevision, s)
	}
}

func (s EconomicWorkStatus) IsTerminal() bool {
	return s == EconomicWorkStatusCompleted || s == EconomicWorkStatusFailed
}

// EconomicWorkReason is the bounded, closed reason vocabulary recorded for a
// retry or a terminal failure. Free-form error text is never the durable
// reason; it is bounded separately in last_error.
type EconomicWorkReason string

const (
	EconomicWorkReasonUnclassified       EconomicWorkReason = "unclassified"
	EconomicWorkReasonTransientFailure   EconomicWorkReason = "transient_failure"
	EconomicWorkReasonRaterFailure       EconomicWorkReason = "rater_failure"
	EconomicWorkReasonReconcilerFailure  EconomicWorkReason = "reconciler_failure"
	EconomicWorkReasonPersistenceFailure EconomicWorkReason = "persistence_failure"
	EconomicWorkReasonDependencyPending  EconomicWorkReason = "dependency_pending"
	EconomicWorkReasonLeaseExpired       EconomicWorkReason = "lease_expired"
	EconomicWorkReasonPermanentFailure   EconomicWorkReason = "permanent_failure"
)

// AllEconomicWorkReasons returns every documented reason in stable order.
func AllEconomicWorkReasons() []EconomicWorkReason {
	return []EconomicWorkReason{
		EconomicWorkReasonUnclassified,
		EconomicWorkReasonTransientFailure,
		EconomicWorkReasonRaterFailure,
		EconomicWorkReasonReconcilerFailure,
		EconomicWorkReasonPersistenceFailure,
		EconomicWorkReasonDependencyPending,
		EconomicWorkReasonLeaseExpired,
		EconomicWorkReasonPermanentFailure,
	}
}

func (r EconomicWorkReason) String() string { return string(r) }

func (r EconomicWorkReason) Validate() error {
	switch r {
	case EconomicWorkReasonUnclassified, EconomicWorkReasonTransientFailure, EconomicWorkReasonRaterFailure,
		EconomicWorkReasonReconcilerFailure, EconomicWorkReasonPersistenceFailure, EconomicWorkReasonDependencyPending,
		EconomicWorkReasonLeaseExpired, EconomicWorkReasonPermanentFailure:
		return nil
	default:
		return fmt.Errorf("%w: unknown economic work reason %q", ErrInvalidEconomicRevision, r)
	}
}

const (
	// MaxEconomicWorkReasonLength bounds any durable free-form failure text.
	MaxEconomicWorkReasonLength = 256
	// MaxEconomicJobDependencies bounds the dependency set of one job.
	MaxEconomicJobDependencies = 8
	// MaxEconomicRevisionClaimBatchSize bounds one claim batch.
	MaxEconomicRevisionClaimBatchSize = 256
	// DefaultEconomicRevisionClaimBatchSize is used when no batch bound is
	// supplied.
	DefaultEconomicRevisionClaimBatchSize = 32
)

// BoundedEconomicWorkReasonText trims and truncates free-form error text so a
// durable delivery row cannot grow without bound.
func BoundedEconomicWorkReasonText(text string) string {
	trimmed := strings.TrimSpace(text)
	runes := []rune(trimmed)
	if len(runes) <= MaxEconomicWorkReasonLength {
		return trimmed
	}
	return string(runes[:MaxEconomicWorkReasonLength])
}

// EconomicJobDependency is an explicit immutable output reference that
// separated reconciliation work depends on. The reference is the full
// revision identity (queue, head, evidence revision, observation input-set
// hash and allocation derivation hash) of a rating output, so a corrected
// rating is a new dependency rather than an overwrite of the previous output.
// DerivationHash is the full allocation-aware valuation input identity. It is
// empty for legacy observation-only outputs so the historical key preimage is
// byte-for-byte unchanged; allocation-aware producers must carry the exact
// DerivationHash from their revision identity and consumers must never guess
// it from the observation hash.
type EconomicJobDependency struct {
	Kind             EconomicWorkKind `json:"kind"`
	Queue            EconomicQueue    `json:"queue"`
	HeadKey          string           `json:"head_key"`
	EvidenceRevision uint64           `json:"evidence_revision"`
	InputSetHash     string           `json:"input_set_hash"`
	DerivationHash   string           `json:"derivation_hash,omitempty"`
}

// NewEconomicJobDependency builds the exact immutable output reference for one
// rating revision identity. The returned dependency carries the producer's
// full derivation identity (including DerivationHash) so durable probes and
// loads resolve the allocation-aware valuation rather than the
// observation-only key or a stale historical output.
func NewEconomicJobDependency(kind EconomicWorkKind, identity EconomicRevisionIdentity) (EconomicJobDependency, error) {
	if err := kind.Validate(); err != nil {
		return EconomicJobDependency{}, fmt.Errorf("%w: dependency kind: %v", ErrInvalidEconomicRevision, err)
	}
	if !kind.IsRating() {
		return EconomicJobDependency{}, fmt.Errorf("%w: dependency kind %q is not a rating output", ErrInvalidEconomicRevision, kind)
	}
	queue, err := kind.Queue()
	if err != nil {
		return EconomicJobDependency{}, fmt.Errorf("%w: dependency queue: %v", ErrInvalidEconomicRevision, err)
	}
	if queue != identity.Queue {
		return EconomicJobDependency{}, fmt.Errorf("%w: dependency kind %q does not belong to queue %q", ErrInvalidEconomicRevision, kind, identity.Queue)
	}
	if err := identity.Validate(); err != nil {
		return EconomicJobDependency{}, fmt.Errorf("%w: dependency identity: %v", ErrInvalidEconomicRevision, err)
	}
	dependency := EconomicJobDependency{
		Kind: kind, Queue: identity.Queue, HeadKey: identity.HeadKey,
		EvidenceRevision: identity.EvidenceRevision, InputSetHash: identity.InputSetHash,
		DerivationHash: identity.DerivationHash,
	}
	if err := dependency.Validate(); err != nil {
		return EconomicJobDependency{}, err
	}
	return dependency, nil
}

// Validate checks that the dependency is a well-formed rating output reference.
func (d EconomicJobDependency) Validate() error {
	if err := d.Kind.Validate(); err != nil {
		return fmt.Errorf("%w: dependency kind: %v", ErrInvalidEconomicRevision, err)
	}
	if !d.Kind.IsRating() {
		return fmt.Errorf("%w: dependency kind %q is not a rating output", ErrInvalidEconomicRevision, d.Kind)
	}
	queue, err := d.Kind.Queue()
	if err != nil {
		return fmt.Errorf("%w: dependency queue: %v", ErrInvalidEconomicRevision, err)
	}
	if d.Queue != queue {
		return fmt.Errorf("%w: dependency kind %q does not belong to queue %q", ErrInvalidEconomicRevision, d.Kind, d.Queue)
	}
	if _, err := d.OutputIdentity(); err != nil {
		return err
	}
	return nil
}

// OutputIdentity returns the rating output revision identity the dependency
// points at. The returned identity carries the full derivation identity,
// including DerivationHash for allocation-aware outputs, because a rating
// output is never dependency-anchored but may be allocation-derived.
func (d EconomicJobDependency) OutputIdentity() (EconomicRevisionIdentity, error) {
	identity := EconomicRevisionIdentity{
		Queue: d.Queue, HeadKey: d.HeadKey, EvidenceRevision: d.EvidenceRevision, InputSetHash: d.InputSetHash,
		DerivationHash: d.DerivationHash,
	}
	if err := identity.Validate(); err != nil {
		return EconomicRevisionIdentity{}, fmt.Errorf("%w: dependency output: %v", ErrInvalidEconomicRevision, err)
	}
	return identity, nil
}

// Key returns the deterministic rating output work identity for this
// dependency.
func (d EconomicJobDependency) Key() string {
	identity, err := d.OutputIdentity()
	if err != nil {
		return ""
	}
	return identity.Key()
}

// Equal reports whether two dependencies reference the same immutable output,
// including the allocation derivation.
func (d EconomicJobDependency) Equal(other EconomicJobDependency) bool {
	return d.Kind == other.Kind && d.Queue == other.Queue && d.HeadKey == other.HeadKey &&
		d.EvidenceRevision == other.EvidenceRevision && d.InputSetHash == other.InputSetHash &&
		d.DerivationHash == other.DerivationHash
}

func canonicalEconomicJobDependencies(dependencies []EconomicJobDependency) ([]EconomicJobDependency, error) {
	if len(dependencies) > MaxEconomicJobDependencies {
		return nil, fmt.Errorf("%w: dependency count %d exceeds %d", ErrInvalidEconomicRevision, len(dependencies), MaxEconomicJobDependencies)
	}
	ordered := append([]EconomicJobDependency(nil), dependencies...)
	for i, dependency := range ordered {
		if err := dependency.Validate(); err != nil {
			return nil, fmt.Errorf("%w: dependency %d: %v", ErrInvalidEconomicRevision, i, err)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Key() != ordered[j].Key() {
			return ordered[i].Key() < ordered[j].Key()
		}
		return ordered[i].Kind < ordered[j].Kind
	})
	canonical := ordered[:0]
	for _, dependency := range ordered {
		if len(canonical) > 0 && canonical[len(canonical)-1].Key() == dependency.Key() {
			if !canonical[len(canonical)-1].Equal(dependency) {
				return nil, fmt.Errorf("%w: conflicting dependency %q", ErrInvalidEconomicRevision, dependency.Key())
			}
			continue
		}
		canonical = append(canonical, dependency)
	}
	return canonical, nil
}

// economicDependenciesHash derives the identity extension for
// dependency-anchored work. Two jobs with the same queue/head/revision/input
// hash but different immutable dependency sets must remain distinct
// actionable revisions. The canonical preimage binds the full derivation
// identity: allocation-aware dependencies extend the legacy
// queue/head/revision/observation preimage with the derivation hash, while
// observation-only dependencies keep the historical preimage byte-for-byte
// unchanged.
func economicDependenciesHash(dependencies []EconomicJobDependency) (string, error) {
	if len(dependencies) == 0 {
		return "", nil
	}
	canonical, err := canonicalEconomicJobDependencies(dependencies)
	if err != nil {
		return "", err
	}
	var preimage strings.Builder
	preimage.WriteString("economic-job-dependencies:v1")
	for _, dependency := range canonical {
		preimage.WriteString("\x00")
		preimage.WriteString(dependency.Kind.String())
		preimage.WriteString("\x00")
		preimage.WriteString(dependency.Queue.String())
		preimage.WriteString("\x00")
		preimage.WriteString(dependency.HeadKey)
		preimage.WriteString("\x00")
		preimage.WriteString(strconv.FormatUint(dependency.EvidenceRevision, 10))
		preimage.WriteString("\x00")
		preimage.WriteString(dependency.InputSetHash)
		if dependency.DerivationHash != "" {
			preimage.WriteString("\x00")
			preimage.WriteString(dependency.DerivationHash)
		}
	}
	digest := sha256.Sum256([]byte(preimage.String()))
	return hex.EncodeToString(digest[:]), nil
}

// EconomicRevisionClaimedWork pairs one immutable job with the exclusive
// lease/fence token a worker must present to complete, retry, fail or
// heartbeat it.
type EconomicRevisionClaimedWork struct {
	Work  EconomicRevisionWork
	Claim EconomicRevisionWorkClaim
}

// EconomicRevisionJobQueueStore owns mutable job-queue delivery state for
// bounded worker consumption. Claim, heartbeat, retry and fail all compare the
// owner and fence so a stale worker cannot overwrite a newer worker's
// progress. None of these operations reads or mutates an account balance,
// exposure or financial head.
type EconomicRevisionJobQueueStore interface {
	ClaimEconomicRevisionWorkBatch(context.Context, EconomicQueue, string, time.Duration, int) ([]EconomicRevisionClaimedWork, error)
	HeartbeatEconomicRevisionWork(context.Context, EconomicRevisionWork, EconomicRevisionWorkClaim, time.Duration) (EconomicRevisionWorkClaim, error)
	RetryEconomicRevisionWorkWithReason(context.Context, EconomicRevisionWork, EconomicRevisionWorkClaim, EconomicWorkReason, time.Time) error
	FailEconomicRevisionWork(context.Context, EconomicRevisionWork, EconomicRevisionWorkClaim, EconomicWorkReason) error
}

// EconomicJobDependencyStatus reports whether one immutable dependency output
// is durably present.
type EconomicJobDependencyStatus uint8

const (
	EconomicJobDependencySatisfied EconomicJobDependencyStatus = iota
	EconomicJobDependencyMissing
)

func (s EconomicJobDependencyStatus) String() string {
	switch s {
	case EconomicJobDependencySatisfied:
		return "satisfied"
	case EconomicJobDependencyMissing:
		return "missing"
	default:
		return "unknown"
	}
}

// Validate reports whether the dependency status is part of the closed
// vocabulary.
func (s EconomicJobDependencyStatus) Validate() error {
	switch s {
	case EconomicJobDependencySatisfied, EconomicJobDependencyMissing:
		return nil
	default:
		return fmt.Errorf("%w: unknown dependency status %d", ErrInvalidEconomicRevision, s)
	}
}

// EconomicJobDependencyCheck is one dependency output probe result.
type EconomicJobDependencyCheck struct {
	Dependency EconomicJobDependency
	Status     EconomicJobDependencyStatus
}

// EconomicRevisionBacklog is a bounded operational snapshot for one queue. It
// exposes backlog age, next-attempt timing and incomplete-evidence counts
// without mutating any job.
type EconomicRevisionBacklog struct {
	Queue                  EconomicQueue
	Pending                int
	Processing             int
	Completed              int
	Failed                 int
	IncompleteDependencies int
	OldestPendingAt        time.Time
	OldestPendingAge       time.Duration
	NextAttemptAt          time.Time
}

// EconomicRevisionBacklogReader exposes bounded operational backlog evidence
// and per-work dependency completeness.
type EconomicRevisionBacklogReader interface {
	EconomicRevisionQueueBacklog(context.Context, EconomicQueue) (EconomicRevisionBacklog, error)
	EconomicRevisionDependencyChecks(context.Context, EconomicRevisionWork) ([]EconomicJobDependencyCheck, error)
}
