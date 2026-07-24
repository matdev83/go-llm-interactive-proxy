package runtimehost

import (
	"context"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

// attemptInput is the immutable snapshot Coordinator supplies for one
// admitted attempt transaction (req 6.2, 6.4, 6.10-6.11). Coordinator clones
// mutable source structs at the boundary; attemptRunner treats the value as
// read-only for the duration of Run.
type attemptInput struct {
	Trigger          sdkreload.Trigger
	AttemptID        int64
	ActiveGeneration int64
	ActiveEffective  *config.EffectiveConfig
	ActiveSource     *configsource.ActiveSourceVersion
}

// attemptOutcome is the immutable result of one attemptRunner.Run transaction.
// EffectiveUpdate/SourceUpdate distinguish three cases: a failed or plain
// no-op attempt carries neither (both nil); an effective-identity no-op that
// must still advance the source baseline carries SourceUpdate only; a
// successful publication carries both. Coordinator applies these fields and
// records the terminal result; Run never mutates Coordinator-owned state.
type attemptOutcome struct {
	Result          sdkreload.Result
	EffectiveUpdate *config.EffectiveConfig
	SourceUpdate    *configsource.ActiveSourceVersion
}

// attemptRunnerDeps wires the one-attempt workflow collaborators.
type attemptRunnerDeps struct {
	Source   StableConfigSource
	Loader   EffectiveLoader
	Classify func(active, candidate *config.EffectiveConfig) ([]configreload.SafeChange, error)
	Compile  CandidateCompiler
	Manager  *Manager
	Observer *ReloadObserver
	// ShuttingDown is a narrow, opaque external shutdown predicate (typically
	// the reload-attempt gate's shutdown flag). Nil is treated as false.
	// attemptRunner never references AttemptGate itself; Coordinator supplies
	// this closure so the runner stays independently testable (req 6.11).
	ShuttingDown func() bool
}

// attemptRunner exclusively owns one admitted reload attempt's linear
// read/load/no-op/classify/compile/prepare/retention-admit/publish/rollback
// transaction and internal error-to-canonical-result mapping (req 6.2, 6.4,
// 6.10-6.11, 7.7). It never touches AttemptGate admission/coalescing, never
// mutates Coordinator history/status, and never records terminal results.
type attemptRunner struct {
	source       StableConfigSource
	loader       EffectiveLoader
	classify     func(active, candidate *config.EffectiveConfig) ([]configreload.SafeChange, error)
	compile      CandidateCompiler
	mgr          *Manager
	observer     *ReloadObserver
	shuttingDown func() bool
}

// newAttemptRunner constructs the sole production attemptRunner.
func newAttemptRunner(deps attemptRunnerDeps) *attemptRunner {
	classify := deps.Classify
	if classify == nil {
		classify = configreload.ClassifyEffective
	}
	return &attemptRunner{
		source:       deps.Source,
		loader:       deps.Loader,
		classify:     classify,
		compile:      deps.Compile,
		mgr:          deps.Manager,
		observer:     deps.Observer,
		shuttingDown: deps.ShuttingDown,
	}
}

// isShuttingDown folds the caller-supplied external predicate with the
// runner-owned Manager shutdown flag. Neither check requires an AttemptGate
// reference; the predicate is an opaque func() bool.
func (r *attemptRunner) isShuttingDown() bool {
	if r == nil {
		return true
	}
	if r.shuttingDown != nil && r.shuttingDown() {
		return true
	}
	return r.mgr != nil && r.mgr.ShuttingDown()
}

func (r *attemptRunner) activeGenerationID() int64 {
	if r == nil || r.mgr == nil {
		return 0
	}
	if g := r.mgr.Active(); g != nil {
		return g.ID()
	}
	return 0
}

// Run executes exactly one admitted attempt transaction and returns an
// immutable outcome. Coordinator applies EffectiveUpdate/SourceUpdate and
// records the terminal result; Run never mutates Coordinator-owned state and
// never holds Coordinator or gate locks while invoking collaborators.
func (r *attemptRunner) Run(ctx context.Context, in attemptInput) (out attemptOutcome) {
	res := sdkreload.Result{
		AttemptID:        in.AttemptID,
		ActiveGeneration: in.ActiveGeneration,
	}

	endAttempt := func(sdkreload.Result) {}
	if r.observer != nil {
		ctx, endAttempt = r.observer.BeginAttempt(ctx, in.Trigger, in.AttemptID, in.ActiveGeneration)
	}

	// plane is the un-transferred candidate request plane (pre-prepare); gen
	// is the prepared-but-not-yet-published candidate generation (post-
	// transfer). Exactly one of the two is non-nil at any point where a
	// rollback may be required. Cleanup helpers clear ownership before
	// Close/Discard and recover cleanup panics so a panicking cleanup never
	// escapes Run, never retriggers cleanup, and never overwrites an already
	// selected canonical cancellation/failure result.
	var plane PublishedRequestPlane
	var gen *Generation

	defer func() {
		if recovered := recover(); recovered != nil {
			// Primary workflow panic is canonical even if cleanup also panics.
			closeOwnedPlane(&plane)
			discardOwnedGeneration(&gen)
			out = attemptOutcome{Result: sdkreload.Result{
				Category:         sdkreload.ResultInternalFailed,
				AttemptID:        in.AttemptID,
				ActiveGeneration: r.activeGenerationID(),
				ReasonCategory:   configreload.StagePanic,
			}}
			_ = configreload.SanitizePanicValue(recovered)
		}
		if out.Result.AttemptID == 0 {
			out.Result.AttemptID = in.AttemptID
		}
		if out.Result.ActiveGeneration == 0 {
			out.Result.ActiveGeneration = r.activeGenerationID()
		}
		endAttempt(out.Result)
	}()

	fail := func(cat sdkreload.ResultCategory, reason string) attemptOutcome {
		res.Category = cat
		res.ReasonCategory = reason
		return attemptOutcome{Result: res}
	}
	failFromLoadErr := func(err error) attemptOutcome {
		cat, reason := configreload.MapLoadFailure(err)
		if srcCat, ok := configsource.CategoryOf(err); ok {
			cat, reason = configreload.MapLoadCategory(string(srcCat))
		}
		return fail(cat, reason)
	}
	canceledByContextOrShutdown := func(err error) bool {
		return r.isShuttingDown() || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	}

	if r.isShuttingDown() {
		out = fail(sdkreload.ResultCanceled, configreload.StageShutdown)
		return out
	}
	if err := ctx.Err(); err != nil {
		out = fail(sdkreload.ResultCanceled, configreload.StageShutdown)
		return out
	}

	activeSrc := cloneActiveSource(in.ActiveSource)

	stageCtx, endStage := ctx, func(string) {}
	if r.observer != nil {
		stageCtx, endStage = r.observer.BeginStage(ctx, configreload.StageRead)
	}
	snap, atomicRes, err := r.source.ReadStable(stageCtx, activeSrc)
	if err != nil {
		if canceledByContextOrShutdown(err) {
			out = fail(sdkreload.ResultCanceled, configreload.StageShutdown)
			endStage(string(out.Result.Category))
			return out
		}
		out = failFromLoadErr(err)
		endStage(string(out.Result.Category))
		return out
	}
	endStage("ok")
	if atomicRes == configsource.AtomicNoop {
		out = fail(sdkreload.ResultNoop, configreload.StageNoop)
		return out
	}

	if r.observer != nil {
		stageCtx, endStage = r.observer.BeginStage(ctx, configreload.StageLoad)
	} else {
		stageCtx, endStage = ctx, func(string) {}
	}
	eff, err := r.loader.LoadEffective(stageCtx, snap.Bytes)
	if err != nil {
		if canceledByContextOrShutdown(err) {
			out = fail(sdkreload.ResultCanceled, configreload.StageShutdown)
			endStage(string(out.Result.Category))
			return out
		}
		out = failFromLoadErr(err)
		endStage(string(out.Result.Category))
		return out
	}
	endStage("ok")

	activeEff := in.ActiveEffective
	if activeEff != nil && activeEff.Identity.PrivateDigest == eff.Identity.PrivateDigest {
		// AtomicEligible may have landed a new inode whose effective identity
		// matches the active generation. Advance the source baseline (without
		// publishing) so a later in-place rewrite of that inode is rejected as
		// non-atomic (req 2.9). This is the one outcome shape that carries
		// SourceUpdate without EffectiveUpdate.
		res.Category = sdkreload.ResultNoop
		res.ReasonCategory = configreload.StageNoop
		out = attemptOutcome{
			Result: res,
			SourceUpdate: &configsource.ActiveSourceVersion{
				HandleIdentity: snap.HandleIdentity,
				PrivateDigest:  snap.PrivateDigest,
			},
		}
		return out
	}

	if activeEff != nil {
		if r.observer != nil {
			_, endStage = r.observer.BeginStage(ctx, configreload.StageClassify)
		} else {
			endStage = func(string) {}
		}
		_, err := r.classify(activeEff, eff)
		if err != nil {
			var rr *configreload.RestartRequiredError
			if errors.As(err, &rr) {
				res.Category = sdkreload.ResultRestartRequired
				res.ReasonCategory = configreload.StageClassify
				if rr != nil {
					res.RestartFields = append([]string(nil), rr.RestartRequiredFields...)
					res.RestartFieldCount = rr.TotalBlocked
				}
				endStage(string(res.Category))
				out = attemptOutcome{Result: res}
				return out
			}
			out = fail(sdkreload.ResultInvalid, configreload.StageClassify)
			endStage(string(out.Result.Category))
			return out
		}
		endStage("ok")
	}

	if r.isShuttingDown() {
		out = fail(sdkreload.ResultCanceled, configreload.StageShutdown)
		return out
	}

	liveKinds := r.collectLiveFactoryKinds()
	if r.observer != nil {
		stageCtx, endStage = r.observer.BeginStage(ctx, configreload.StageCompile)
	} else {
		stageCtx, endStage = ctx, func(string) {}
	}
	var compileErr error
	plane, compileErr = r.compileIsolated(stageCtx, eff.Config, liveKinds)
	if compileErr != nil {
		var rr *configreload.RestartRequiredError
		if errors.As(compileErr, &rr) {
			res.Category = sdkreload.ResultRestartRequired
			res.ReasonCategory = configreload.StageCompile
			if rr != nil {
				res.RestartFields = append([]string(nil), rr.RestartRequiredFields...)
				res.RestartFieldCount = rr.TotalBlocked
			}
			endStage(string(res.Category))
			out = attemptOutcome{Result: res}
			return out
		}
		if canceledByContextOrShutdown(compileErr) {
			out = fail(sdkreload.ResultCanceled, configreload.StageShutdown)
			endStage(string(out.Result.Category))
			return out
		}
		if errors.Is(compileErr, errCompilePanic) {
			out = fail(sdkreload.ResultInternalFailed, configreload.StagePanic)
			endStage(string(out.Result.Category))
			return out
		}
		out = fail(sdkreload.ResultPreparationFailed, configreload.StageCompile)
		endStage(string(out.Result.Category))
		return out
	}
	endStage("ok")
	if plane == nil {
		out = fail(sdkreload.ResultPreparationFailed, configreload.StageCompile)
		return out
	}

	if r.isShuttingDown() {
		closeOwnedPlane(&plane)
		out = fail(sdkreload.ResultCanceled, configreload.StageShutdown)
		return out
	}
	if err := ctx.Err(); err != nil {
		closeOwnedPlane(&plane)
		out = fail(sdkreload.ResultCanceled, configreload.StageShutdown)
		return out
	}

	if r.observer != nil {
		_, endStage = r.observer.BeginStage(ctx, configreload.StagePrepare)
	} else {
		endStage = func(string) {}
	}
	label := string(in.Trigger.Kind)
	if label == "" {
		label = "reload"
	}
	gen = r.mgr.PrepareRequestPlane(label, plane)
	plane = nil // ownership transferred: cleanup now goes through gen.Discard, never plane.Close again.
	gen.SetMetaHints(MetaHints{
		PublicFingerprint: eff.Identity.PublicFingerprint,
		TriggerKind:       string(in.Trigger.Kind),
		LoadedAt:          eff.LoadedAt,
	})
	endStage("ok")

	if r.isShuttingDown() {
		discardOwnedGeneration(&gen)
		out = fail(sdkreload.ResultCanceled, configreload.StageShutdown)
		return out
	}

	if r.observer != nil {
		_, endStage = r.observer.BeginStage(ctx, configreload.StagePublish)
	} else {
		endStage = func(string) {}
	}
	publishErr := r.mgr.Publish(gen)
	var publishedGenID int64
	if publishErr == nil {
		publishedGenID = gen.ID()
	}
	gen = nil // Manager now owns cleanup either way: swapped in, or already Discarded on rejection.
	if publishErr != nil {
		switch {
		case errors.Is(publishErr, ErrRetentionBlocked):
			res.Category = sdkreload.ResultRetentionBlocked
			res.ReasonCategory = configreload.StageRetention
		case errors.Is(publishErr, ErrHostShuttingDown):
			res.Category = sdkreload.ResultCanceled
			res.ReasonCategory = configreload.StageShutdown
		default:
			res.Category = sdkreload.ResultPreparationFailed
			res.ReasonCategory = configreload.StagePublish
		}
		res.ActiveGeneration = r.activeGenerationID()
		endStage(string(res.Category))
		out = attemptOutcome{Result: res}
		return out
	}
	endStage(string(sdkreload.ResultPublished))

	res.Category = sdkreload.ResultPublished
	res.PreviousGeneration = in.ActiveGeneration
	res.ActiveGeneration = publishedGenID
	res.ReasonCategory = configreload.StagePublish
	out = attemptOutcome{
		Result:          res,
		EffectiveUpdate: eff,
		SourceUpdate: &configsource.ActiveSourceVersion{
			HandleIdentity: snap.HandleIdentity,
			PrivateDigest:  snap.PrivateDigest,
		},
	}
	return out
}

// closeOwnedPlane transfers ownership out of *owned before Close so a
// panicking/failed Close cannot be retried by a later cleanup path. Cleanup
// panics are recovered and sanitized here; they never escape Run.
func closeOwnedPlane(owned *PublishedRequestPlane) {
	if owned == nil {
		return
	}
	p := *owned
	*owned = nil
	if p == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = configreload.SanitizePanicValue(recovered)
		}
	}()
	_ = p.Close()
}

// discardOwnedGeneration transfers ownership out of *owned before Discard so
// Generation-owned plane Close (inside Discard) runs at most once from the
// runner and cleanup panics never escape Run. After Prepare transfer the
// runner never calls plane.Close directly.
func discardOwnedGeneration(owned **Generation) {
	if owned == nil {
		return
	}
	g := *owned
	*owned = nil
	if g == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = configreload.SanitizePanicValue(recovered)
		}
	}()
	_ = g.Discard()
}

// collectLiveFactoryKinds sums BackendFactoryKindCounts across the active and
// retained generations (tasks.md Implementation Notes; req 8.8).
func (r *attemptRunner) collectLiveFactoryKinds() map[string]int {
	if r == nil || r.mgr == nil {
		return nil
	}
	out := make(map[string]int)
	add := func(g *Generation) {
		if g == nil {
			return
		}
		plane := g.RequestPlane()
		counter, ok := plane.(BackendFactoryKindCounter)
		if !ok || counter == nil {
			return
		}
		for k, n := range counter.BackendFactoryKindCounts() {
			if k == "" || n <= 0 {
				continue
			}
			out[k] += n
		}
	}
	add(r.mgr.Active())
	for _, g := range r.mgr.SnapshotRetained() {
		add(g)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// errCompilePanic sentinels a recovered panic from CandidateCompiler.Compile
// so it maps to the canonical internal-failed/StagePanic result.
var errCompilePanic = errors.New("runtimehost: candidate compile panic")

func (r *attemptRunner) compileIsolated(ctx context.Context, cfg *config.Config, liveKinds map[string]int) (plane PublishedRequestPlane, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			plane = nil
			err = fmt.Errorf("%w: %s", errCompilePanic, configreload.SanitizePanicValue(recovered))
		}
	}()
	return r.compile.Compile(ctx, cfg, liveKinds)
}
