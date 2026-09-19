package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// ErrInvalidEconomicJobRunner identifies an incomplete or malformed
// application runner configuration.
var ErrInvalidEconomicJobRunner = errors.New("billing: invalid economic job runner")

// ErrEconomicRevisionDependencyOutputMissing identifies dependency-anchored
// work whose immutable rating output is not durable yet. It is retryable with
// the bounded dependency_pending reason rather than a terminal failure.
var ErrEconomicRevisionDependencyOutputMissing = errors.New("billing: economic revision dependency output missing")

// EconomicJobDependencyOutput is one exact immutable rating output loaded for
// dependency-anchored reconciliation work.
type EconomicJobDependencyOutput struct {
	Dependency EconomicJobDependency
	Valuation  economics.Valuation
}

// EconomicJobReconciler computes a pure reconciliation envelope over the exact
// immutable dependency outputs of provider-queue reconciliation work. It must
// not perform monetary posting or mutate customer state.
type EconomicJobReconciler interface {
	ReconcileJob(context.Context, EconomicRevisionWork, []EconomicJobDependencyOutput) (*EconomicReconciliation, error)
}

// EconomicRevisionDependencyOutputReader loads one immutable dependency rating
// output by its exact revision identity. A missing output must fail with
// ErrEconomicRevisionDependencyOutputMissing.
type EconomicRevisionDependencyOutputReader interface {
	LoadEconomicRevisionDependencyOutput(context.Context, EconomicJobDependency) (economics.Valuation, error)
}

// EconomicRevisionReconciliationStore persists dependency-anchored
// reconciliation output idempotently and probes whether it already exists.
// Persistence must never touch a customer balance, journal or financial head.
type EconomicRevisionReconciliationStore interface {
	AppendEconomicRevisionReconciliation(context.Context, EconomicRevisionWork, EconomicReconciliation) error
	HasEconomicRevisionReconciliation(context.Context, EconomicRevisionIdentity) (bool, error)
}

// EconomicJobQueueStore is the consumer-owned queue port for one bounded
// worker run: claim a batch, then complete, retry or fail each claim with its
// exact fence.
type EconomicJobQueueStore interface {
	ClaimEconomicRevisionWorkBatch(context.Context, EconomicQueue, string, time.Duration, int) ([]EconomicRevisionClaimedWork, error)
	CompleteEconomicRevisionWork(context.Context, EconomicRevisionWork, EconomicRevisionWorkClaim) error
	RetryEconomicRevisionWorkWithReason(context.Context, EconomicRevisionWork, EconomicRevisionWorkClaim, EconomicWorkReason, time.Time) error
	FailEconomicRevisionWork(context.Context, EconomicRevisionWork, EconomicRevisionWorkClaim, EconomicWorkReason) error
}

// EconomicJobFailureStage identifies the orchestration stage that failed so a
// failure can be classified against the approved retry/terminal policy.
type EconomicJobFailureStage uint8

const (
	EconomicJobFailureStageClaim EconomicJobFailureStage = iota
	EconomicJobFailureStageProbe
	EconomicJobFailureStageDependencyLoad
	EconomicJobFailureStageRate
	EconomicJobFailureStageReconcile
	EconomicJobFailureStageNormalize
	EconomicJobFailureStagePersist
	EconomicJobFailureStageFinalize
)

// AllEconomicJobFailureStages returns every documented failure stage in stable
// order.
func AllEconomicJobFailureStages() []EconomicJobFailureStage {
	return []EconomicJobFailureStage{
		EconomicJobFailureStageClaim, EconomicJobFailureStageProbe, EconomicJobFailureStageDependencyLoad,
		EconomicJobFailureStageRate, EconomicJobFailureStageReconcile, EconomicJobFailureStageNormalize,
		EconomicJobFailureStagePersist, EconomicJobFailureStageFinalize,
	}
}

func (s EconomicJobFailureStage) String() string {
	switch s {
	case EconomicJobFailureStageClaim:
		return "claim"
	case EconomicJobFailureStageProbe:
		return "probe"
	case EconomicJobFailureStageDependencyLoad:
		return "dependency_load"
	case EconomicJobFailureStageRate:
		return "rate"
	case EconomicJobFailureStageReconcile:
		return "reconcile"
	case EconomicJobFailureStageNormalize:
		return "normalize"
	case EconomicJobFailureStagePersist:
		return "persist"
	case EconomicJobFailureStageFinalize:
		return "finalize"
	default:
		return "unknown"
	}
}

// Validate reports whether the stage is part of the closed vocabulary.
func (s EconomicJobFailureStage) Validate() error {
	switch s {
	case EconomicJobFailureStageClaim, EconomicJobFailureStageProbe, EconomicJobFailureStageDependencyLoad,
		EconomicJobFailureStageRate, EconomicJobFailureStageReconcile, EconomicJobFailureStageNormalize,
		EconomicJobFailureStagePersist, EconomicJobFailureStageFinalize:
		return nil
	default:
		return fmt.Errorf("%w: unknown economic job failure stage %d", ErrInvalidEconomicJobRunner, s)
	}
}

// EconomicJobFailureDisposition is the closed retry/terminal classification.
type EconomicJobFailureDisposition uint8

const (
	EconomicJobFailureRetry EconomicJobFailureDisposition = iota
	EconomicJobFailureTerminal
)

func (d EconomicJobFailureDisposition) String() string {
	switch d {
	case EconomicJobFailureRetry:
		return "retry"
	case EconomicJobFailureTerminal:
		return "terminal"
	default:
		return "unknown"
	}
}

// Validate reports whether the disposition is part of the closed vocabulary.
func (d EconomicJobFailureDisposition) Validate() error {
	switch d {
	case EconomicJobFailureRetry, EconomicJobFailureTerminal:
		return nil
	default:
		return fmt.Errorf("%w: unknown economic job failure disposition %d", ErrInvalidEconomicJobRunner, d)
	}
}

// EconomicJobFailureClass is one classified attempt failure.
type EconomicJobFailureClass struct {
	Stage       EconomicJobFailureStage
	Reason      EconomicWorkReason
	Disposition EconomicJobFailureDisposition
}

// Terminal reports whether the failure retires the work instead of retrying.
func (c EconomicJobFailureClass) Terminal() bool {
	return c.Disposition == EconomicJobFailureTerminal
}

// ClassifyEconomicJobFailure maps one stage failure to the bounded reason and
// retry/terminal policy: deterministic invalid, mismatched or incomparable
// inputs are terminal; missing dependency outputs and transient failures
// retry with a bounded next attempt; cancellation releases through the
// lease-expiry retry path.
func ClassifyEconomicJobFailure(stage EconomicJobFailureStage, err error) EconomicJobFailureClass {
	if err == nil {
		return EconomicJobFailureClass{Stage: stage, Reason: EconomicWorkReasonUnclassified, Disposition: EconomicJobFailureRetry}
	}
	if errors.Is(err, ErrEconomicRevisionDependencyOutputMissing) {
		return EconomicJobFailureClass{Stage: stage, Reason: EconomicWorkReasonDependencyPending, Disposition: EconomicJobFailureRetry}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return EconomicJobFailureClass{Stage: stage, Reason: EconomicWorkReasonLeaseExpired, Disposition: EconomicJobFailureRetry}
	}
	if economicJobDeterministicFailure(err) {
		return EconomicJobFailureClass{Stage: stage, Reason: EconomicWorkReasonPermanentFailure, Disposition: EconomicJobFailureTerminal}
	}
	return EconomicJobFailureClass{Stage: stage, Reason: economicJobStageRetryReason(stage), Disposition: EconomicJobFailureRetry}
}

func economicJobDeterministicFailure(err error) bool {
	for _, sentinel := range []error{
		ErrInvalidEconomicRevision, ErrEconomicRevisionSubjectMismatch, ErrEconomicRevisionBasisMismatch,
		ErrEconomicRevisionInputMismatch, ErrEconomicRevisionConflict, ErrEconomicRevisionFence,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

func economicJobStageRetryReason(stage EconomicJobFailureStage) EconomicWorkReason {
	switch stage {
	case EconomicJobFailureStageRate:
		return EconomicWorkReasonRaterFailure
	case EconomicJobFailureStageReconcile:
		return EconomicWorkReasonReconcilerFailure
	case EconomicJobFailureStageProbe, EconomicJobFailureStageDependencyLoad, EconomicJobFailureStagePersist, EconomicJobFailureStageFinalize:
		return EconomicWorkReasonPersistenceFailure
	default:
		return EconomicWorkReasonTransientFailure
	}
}

// EconomicJobRunSummary is the bounded outcome of one RunOnce invocation. The
// embedded backlog snapshot carries the queue age and incomplete-evidence
// diagnostics. Completed/Retried/Failed count durably recorded transitions;
// Superseded counts claims won by another worker; Released counts claims
// released unstarted because the caller context was canceled.
type EconomicJobRunSummary struct {
	Queue      EconomicQueue
	Claimed    int
	Completed  int
	Retried    int
	Failed     int
	Superseded int
	Released   int
	Backlog    EconomicRevisionBacklog
}

// EconomicJobRunnerConfig declares every port and bound of the application
// runner. All ports are required; the runner owns no goroutine and performs
// exactly one bounded claim batch per RunOnce call.
type EconomicJobRunnerConfig struct {
	Queue           EconomicJobQueueStore
	Backlog         EconomicRevisionBacklogReader
	Results         EconomicRevisionResultStore
	Dependencies    EconomicRevisionDependencyOutputReader
	Reconciliations EconomicRevisionReconciliationStore
	Rater           PostUsageRater
	Reconciler      EconomicJobReconciler
	Owner           string
	Batch           int
	Lease           time.Duration
	RetryBackoff    time.Duration
	Now             func() time.Time
}

const maxEconomicJobRunnerRetryBackoff = 24 * time.Hour

var economicJobRunnerSequence atomic.Uint64

// EconomicJobRunner dispatches one bounded claim batch per queue by closed
// work kind to injected pure ports. Computation runs outside any database
// transaction; immutable outputs are persisted idempotently and the claim is
// finalized with its exact fence. No financial transition is performed here.
type EconomicJobRunner struct {
	queue           EconomicJobQueueStore
	backlog         EconomicRevisionBacklogReader
	results         EconomicRevisionResultStore
	resultsProbe    EconomicRevisionResultProbe
	dependencies    EconomicRevisionDependencyOutputReader
	reconciliations EconomicRevisionReconciliationStore
	rater           PostUsageRater
	reconciler      EconomicJobReconciler
	owner           string
	batch           int
	lease           time.Duration
	retryBackoff    time.Duration
	now             func() time.Time
}

// NewEconomicJobRunner validates and constructs the application runner.
func NewEconomicJobRunner(cfg EconomicJobRunnerConfig) (*EconomicJobRunner, error) {
	if cfg.Queue == nil || cfg.Backlog == nil || cfg.Results == nil || cfg.Dependencies == nil || cfg.Reconciliations == nil || cfg.Rater == nil || cfg.Reconciler == nil {
		return nil, fmt.Errorf("%w: queue, backlog, result, dependency, reconciliation, rater and reconciler ports are required", ErrInvalidEconomicJobRunner)
	}
	resultsProbe, ok := cfg.Results.(EconomicRevisionResultProbe)
	if !ok {
		return nil, fmt.Errorf("%w: result store must implement EconomicRevisionResultProbe", ErrInvalidEconomicJobRunner)
	}
	owner := strings.TrimSpace(cfg.Owner)
	if owner == "" {
		owner = fmt.Sprintf("economic-job-runner-%d", economicJobRunnerSequence.Add(1))
	}
	batch := cfg.Batch
	if batch <= 0 {
		batch = DefaultEconomicRevisionClaimBatchSize
	}
	if batch > MaxEconomicRevisionClaimBatchSize {
		return nil, fmt.Errorf("%w: batch %d exceeds %d", ErrInvalidEconomicJobRunner, batch, MaxEconomicRevisionClaimBatchSize)
	}
	lease := cfg.Lease
	if lease <= 0 {
		lease = economicRevisionClaimLease
	}
	if cfg.RetryBackoff < 0 || cfg.RetryBackoff > maxEconomicJobRunnerRetryBackoff {
		return nil, fmt.Errorf("%w: retry backoff %s is out of range", ErrInvalidEconomicJobRunner, cfg.RetryBackoff)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &EconomicJobRunner{
		queue: cfg.Queue, backlog: cfg.Backlog, results: cfg.Results, resultsProbe: resultsProbe,
		dependencies: cfg.Dependencies, reconciliations: cfg.Reconciliations, rater: cfg.Rater, reconciler: cfg.Reconciler,
		owner: owner, batch: batch, lease: lease, retryBackoff: cfg.RetryBackoff, now: now,
	}, nil
}

// RunOnce claims and processes at most one bounded batch from the explicit
// queue. Per-item failures are recorded durably and counted in the summary;
// the returned error reports batch-level failures, cancellation or a release
// fault that could not be recorded.
func (r *EconomicJobRunner) RunOnce(ctx context.Context, queue EconomicQueue) (EconomicJobRunSummary, error) {
	summary := EconomicJobRunSummary{Queue: queue}
	if r == nil || r.queue == nil {
		return summary, fmt.Errorf("%w: nil economic job runner", ErrInvalidEconomicJobRunner)
	}
	if err := queue.Validate(); err != nil {
		return summary, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	claims, err := r.queue.ClaimEconomicRevisionWorkBatch(ctx, queue, r.owner, r.lease, r.batch)
	if err != nil {
		return summary, fmt.Errorf("billing: claim %s economic jobs: %w", queue, err)
	}
	summary.Claimed = len(claims)
	for _, claimed := range claims {
		if err := ctx.Err(); err != nil {
			if releaseErr := r.releaseUnstarted(ctx, claimed, &summary); releaseErr != nil {
				return r.finish(ctx, summary, errors.Join(err, releaseErr))
			}
			return r.finish(ctx, summary, err)
		}
		if err := r.processClaimed(ctx, claimed, &summary); err != nil {
			return r.finish(ctx, summary, err)
		}
	}
	return r.finish(ctx, summary, ctx.Err())
}

func (r *EconomicJobRunner) finish(ctx context.Context, summary EconomicJobRunSummary, runErr error) (EconomicJobRunSummary, error) {
	diagnosticCtx, cancel := r.claimContext(ctx)
	defer cancel()
	backlog, err := r.backlog.EconomicRevisionQueueBacklog(diagnosticCtx, summary.Queue)
	if err != nil {
		backlogErr := fmt.Errorf("billing: %s economic job backlog: %w", summary.Queue, err)
		if runErr != nil {
			return summary, errors.Join(runErr, backlogErr)
		}
		return summary, backlogErr
	}
	summary.Backlog = backlog
	return summary, runErr
}

func (r *EconomicJobRunner) processClaimed(ctx context.Context, claimed EconomicRevisionClaimedWork, summary *EconomicJobRunSummary) error {
	work, err := claimed.Work.Normalize()
	if err != nil {
		return r.handleFailure(ctx, claimed.Work, claimed.Claim, EconomicJobFailureStageNormalize, err, summary)
	}
	if work.Queue != summary.Queue {
		return r.handleFailure(ctx, work, claimed.Claim, EconomicJobFailureStageNormalize,
			fmt.Errorf("%w: claimed queue %q", ErrInvalidEconomicRevision, work.Queue), summary)
	}
	identity, err := work.Identity()
	if err != nil {
		return r.handleFailure(ctx, work, claimed.Claim, EconomicJobFailureStageNormalize, err, summary)
	}
	switch work.Kind {
	case EconomicWorkKindCustomerRating, EconomicWorkKindProviderRating:
		return r.processRating(ctx, work, identity, claimed.Claim, summary)
	case EconomicWorkKindReconciliation:
		return r.processReconciliation(ctx, work, identity, claimed.Claim, summary)
	default:
		return r.handleFailure(ctx, work, claimed.Claim, EconomicJobFailureStageNormalize,
			fmt.Errorf("%w: unknown work kind %q", ErrInvalidEconomicRevision, work.Kind), summary)
	}
}

func (r *EconomicJobRunner) processRating(ctx context.Context, work EconomicRevisionWork, identity EconomicRevisionIdentity, claim EconomicRevisionWorkClaim, summary *EconomicJobRunSummary) error {
	processed, err := r.resultsProbe.HasEconomicRevisionResult(ctx, identity)
	if err != nil {
		return r.handleFailure(ctx, work, claim, EconomicJobFailureStageProbe, err, summary)
	}
	if !processed {
		valuation, err := r.rater.Rate(ctx, work.Input.Clone())
		if err != nil {
			return r.handleFailure(ctx, work, claim, EconomicJobFailureStageRate, err, summary)
		}
		valuation, err = normalizeRevisionValuation(work, identity, valuation)
		if err != nil {
			return r.handleFailure(ctx, work, claim, EconomicJobFailureStageNormalize, err, summary)
		}
		if err := r.results.AppendEconomicRevisionResult(ctx, work, EconomicRevisionResult{Valuation: valuation}); err != nil {
			return r.handleFailure(ctx, work, claim, EconomicJobFailureStagePersist, err, summary)
		}
	}
	return r.completeClaim(ctx, work, claim, summary)
}

func (r *EconomicJobRunner) processReconciliation(ctx context.Context, work EconomicRevisionWork, identity EconomicRevisionIdentity, claim EconomicRevisionWorkClaim, summary *EconomicJobRunSummary) error {
	processed, err := r.reconciliations.HasEconomicRevisionReconciliation(ctx, identity)
	if err != nil {
		return r.handleFailure(ctx, work, claim, EconomicJobFailureStageProbe, err, summary)
	}
	if !processed {
		outputs := make([]EconomicJobDependencyOutput, 0, len(work.Dependencies))
		for _, dependency := range work.Dependencies {
			valuation, err := r.dependencies.LoadEconomicRevisionDependencyOutput(ctx, dependency)
			if err != nil {
				return r.handleFailure(ctx, work, claim, EconomicJobFailureStageDependencyLoad, err, summary)
			}
			if err := validateEconomicJobDependencyOutput(dependency, valuation); err != nil {
				return r.handleFailure(ctx, work, claim, EconomicJobFailureStageDependencyLoad, err, summary)
			}
			outputs = append(outputs, EconomicJobDependencyOutput{Dependency: dependency, Valuation: valuation})
		}
		reconciliation, err := r.reconciler.ReconcileJob(ctx, work, outputs)
		if err != nil {
			return r.handleFailure(ctx, work, claim, EconomicJobFailureStageReconcile, err, summary)
		}
		if reconciliation == nil {
			return r.handleFailure(ctx, work, claim, EconomicJobFailureStageReconcile, errors.New("billing: reconciliation executor returned no result"), summary)
		}
		normalized, err := normalizeRevisionReconciliation(work, identity, *reconciliation)
		if err != nil {
			return r.handleFailure(ctx, work, claim, EconomicJobFailureStageNormalize, err, summary)
		}
		if err := r.reconciliations.AppendEconomicRevisionReconciliation(ctx, work, *normalized); err != nil {
			return r.handleFailure(ctx, work, claim, EconomicJobFailureStagePersist, err, summary)
		}
	}
	return r.completeClaim(ctx, work, claim, summary)
}

func validateEconomicJobDependencyOutput(dependency EconomicJobDependency, valuation economics.Valuation) error {
	output, err := dependency.OutputIdentity()
	if err != nil {
		return err
	}
	if valuation.ID != output.ValuationKey() {
		return fmt.Errorf("%w: dependency output id got=%q want=%q", ErrEconomicRevisionInputMismatch, valuation.ID, output.ValuationKey())
	}
	if valuation.Version != economics.ValuationVersionV2 {
		return fmt.Errorf("%w: dependency output version got=%d", ErrEconomicRevisionInputMismatch, valuation.Version)
	}
	if valuation.InputSetHash != dependency.InputSetHash {
		return fmt.Errorf("%w: dependency output hash got=%q want=%q", ErrEconomicRevisionInputMismatch, valuation.InputSetHash, dependency.InputSetHash)
	}
	return nil
}

func (r *EconomicJobRunner) completeClaim(ctx context.Context, work EconomicRevisionWork, claim EconomicRevisionWorkClaim, summary *EconomicJobRunSummary) error {
	releaseCtx, cancel := r.claimContext(ctx)
	defer cancel()
	if err := r.queue.CompleteEconomicRevisionWork(releaseCtx, work, claim); err != nil {
		if errors.Is(err, ErrEconomicRevisionClaimLost) {
			summary.Superseded++
			return nil
		}
		return r.handleFailure(ctx, work, claim, EconomicJobFailureStageFinalize, err, summary)
	}
	summary.Completed++
	return nil
}

func (r *EconomicJobRunner) handleFailure(ctx context.Context, work EconomicRevisionWork, claim EconomicRevisionWorkClaim, stage EconomicJobFailureStage, cause error, summary *EconomicJobRunSummary) error {
	class := ClassifyEconomicJobFailure(stage, cause)
	releaseCtx, cancel := r.claimContext(ctx)
	defer cancel()
	if class.Terminal() {
		if err := r.queue.FailEconomicRevisionWork(releaseCtx, work, claim, class.Reason); err != nil {
			if errors.Is(err, ErrEconomicRevisionClaimLost) {
				summary.Superseded++
				return nil
			}
			return fmt.Errorf("billing: fail %s economic job: %w", summary.Queue, err)
		}
		summary.Failed++
		return nil
	}
	if err := r.queue.RetryEconomicRevisionWorkWithReason(releaseCtx, work, claim, class.Reason, r.nextAttemptAt()); err != nil {
		if errors.Is(err, ErrEconomicRevisionClaimLost) {
			summary.Superseded++
			return nil
		}
		return fmt.Errorf("billing: retry %s economic job: %w", summary.Queue, err)
	}
	summary.Retried++
	return nil
}

func (r *EconomicJobRunner) releaseUnstarted(ctx context.Context, claimed EconomicRevisionClaimedWork, summary *EconomicJobRunSummary) error {
	releaseCtx, cancel := r.claimContext(ctx)
	defer cancel()
	if err := r.queue.RetryEconomicRevisionWorkWithReason(releaseCtx, claimed.Work, claimed.Claim, EconomicWorkReasonLeaseExpired, r.now().UTC()); err != nil {
		if errors.Is(err, ErrEconomicRevisionClaimLost) {
			summary.Superseded++
			return nil
		}
		return fmt.Errorf("billing: release %s economic job: %w", summary.Queue, err)
	}
	summary.Released++
	return nil
}

func (r *EconomicJobRunner) nextAttemptAt() time.Time {
	now := r.now().UTC()
	if r.retryBackoff <= 0 {
		return now
	}
	return now.Add(r.retryBackoff)
}

// claimContext keeps a bounded release/finalize attempt possible when the
// caller context is canceled. Computation still honors the caller context;
// only durable claim release and diagnostics are detached and time-limited.
func (r *EconomicJobRunner) claimContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil || ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), economicRevisionStateReleaseWait)
}
