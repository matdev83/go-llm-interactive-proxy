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

// attemptInput is the immutable snapshot Coordinator supplies for one admitted attempt.
type attemptInput struct {
	Trigger          sdkreload.Trigger
	AttemptID        int64
	ActiveGeneration int64
	ActiveEffective  *config.EffectiveConfig
	ActiveSource     *configsource.ActiveSourceVersion
}

// attemptOutcome is the immutable result of one attemptRunner.Run transaction.
type attemptOutcome struct {
	Result          sdkreload.Result
	EffectiveUpdate *config.EffectiveConfig
	SourceUpdate    *configsource.ActiveSourceVersion
}

type attemptRunnerDeps struct {
	Source   StableConfigSource
	Loader   EffectiveLoader
	Classify func(active, candidate *config.EffectiveConfig) ([]configreload.SafeChange, error)
	Compile  CandidateCompiler
	Manager  *Manager
	Observer *ReloadObserver
	// ShuttingDown is a narrow opaque shutdown predicate (typically the gate). Nil is false.
	ShuttingDown func() bool
}

// attemptRunner exclusively owns one admitted reload attempt transaction.
type attemptRunner struct {
	source       StableConfigSource
	loader       EffectiveLoader
	classify     func(active, candidate *config.EffectiveConfig) ([]configreload.SafeChange, error)
	compile      CandidateCompiler
	mgr          *Manager
	observer     *ReloadObserver
	shuttingDown func() bool
}

func newAttemptRunner(deps attemptRunnerDeps) *attemptRunner {
	classify := deps.Classify
	if classify == nil {
		classify = configreload.ClassifyEffective
	}
	return &attemptRunner{
		source: deps.Source, loader: deps.Loader, classify: classify,
		compile: deps.Compile, mgr: deps.Manager, observer: deps.Observer,
		shuttingDown: deps.ShuttingDown,
	}
}

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

func (r *attemptRunner) beginStage(ctx context.Context, stage string) (context.Context, func(string)) {
	if r.observer != nil {
		return r.observer.BeginStage(ctx, stage)
	}
	return ctx, func(string) {}
}

func failOutcome(res *sdkreload.Result, cat sdkreload.ResultCategory, reason string) attemptOutcome {
	res.Category = cat
	res.ReasonCategory = reason
	return attemptOutcome{Result: *res}
}

func failFromLoadErr(res *sdkreload.Result, err error) attemptOutcome {
	cat, reason := configreload.MapLoadFailure(err)
	if srcCat, ok := configsource.CategoryOf(err); ok {
		cat, reason = configreload.MapLoadCategory(string(srcCat))
	}
	return failOutcome(res, cat, reason)
}

func restartRequiredOutcome(res *sdkreload.Result, stage string, rr *configreload.RestartRequiredError) attemptOutcome {
	res.Category = sdkreload.ResultRestartRequired
	res.ReasonCategory = stage
	if rr != nil {
		res.RestartFields = append([]string(nil), rr.RestartRequiredFields...)
		res.RestartFieldCount = rr.TotalBlocked
	}
	return attemptOutcome{Result: *res}
}

func (r *attemptRunner) canceled(err error) bool {
	return r.isShuttingDown() || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// Run executes exactly one admitted attempt transaction and returns an immutable outcome.
func (r *attemptRunner) Run(ctx context.Context, in attemptInput) (out attemptOutcome) {
	res := sdkreload.Result{AttemptID: in.AttemptID, ActiveGeneration: in.ActiveGeneration}
	endAttempt := func(sdkreload.Result) {}
	if r.observer != nil {
		ctx, endAttempt = r.observer.BeginAttempt(ctx, in.Trigger, in.AttemptID, in.ActiveGeneration)
	}

	var plane PublishedRequestPlane
	var gen *Generation
	defer func() {
		if recovered := recover(); recovered != nil {
			closeOwnedPlane(&plane)
			discardOwnedGeneration(&gen)
			out = attemptOutcome{Result: sdkreload.Result{
				Category: sdkreload.ResultInternalFailed, AttemptID: in.AttemptID,
				ActiveGeneration: r.activeGenerationID(), ReasonCategory: configreload.StagePanic,
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

	cancelOut := func() attemptOutcome {
		return failOutcome(&res, sdkreload.ResultCanceled, configreload.StageShutdown)
	}
	if r.isShuttingDown() || ctx.Err() != nil {
		out = cancelOut()
		return out
	}

	activeSrc := cloneActiveSource(in.ActiveSource)
	stageCtx, endStage := r.beginStage(ctx, configreload.StageRead)
	snap, atomicRes, err := r.source.ReadStable(stageCtx, activeSrc)
	if err != nil {
		if r.canceled(err) {
			out = cancelOut()
		} else {
			out = failFromLoadErr(&res, err)
		}
		endStage(string(out.Result.Category))
		return out
	}
	endStage("ok")
	if atomicRes == configsource.AtomicNoop {
		out = failOutcome(&res, sdkreload.ResultNoop, configreload.StageNoop)
		return out
	}

	stageCtx, endStage = r.beginStage(ctx, configreload.StageLoad)
	eff, err := r.loader.LoadEffective(stageCtx, snap.Bytes)
	if err != nil {
		if r.canceled(err) {
			out = cancelOut()
		} else {
			out = failFromLoadErr(&res, err)
		}
		endStage(string(out.Result.Category))
		return out
	}
	endStage("ok")

	srcUpdate := &configsource.ActiveSourceVersion{
		HandleIdentity: snap.HandleIdentity, PrivateDigest: snap.PrivateDigest,
	}
	activeEff := in.ActiveEffective
	if activeEff != nil && activeEff.Identity.PrivateDigest == eff.Identity.PrivateDigest {
		// Advance source baseline without publishing (req 2.9).
		res.Category = sdkreload.ResultNoop
		res.ReasonCategory = configreload.StageNoop
		out = attemptOutcome{Result: res, SourceUpdate: srcUpdate}
		return out
	}

	if activeEff != nil {
		_, endStage = r.beginStage(ctx, configreload.StageClassify)
		_, err := r.classify(activeEff, eff)
		if err != nil {
			var rr *configreload.RestartRequiredError
			if errors.As(err, &rr) {
				out = restartRequiredOutcome(&res, configreload.StageClassify, rr)
				endStage(string(res.Category))
				return out
			}
			out = failOutcome(&res, sdkreload.ResultInvalid, configreload.StageClassify)
			endStage(string(out.Result.Category))
			return out
		}
		endStage("ok")
	}

	if r.isShuttingDown() {
		out = cancelOut()
		return out
	}

	liveKinds := r.collectLiveFactoryKinds()
	stageCtx, endStage = r.beginStage(ctx, configreload.StageCompile)
	plane, compileErr := r.compileIsolated(stageCtx, eff.Config, liveKinds)
	if compileErr != nil {
		var rr *configreload.RestartRequiredError
		if errors.As(compileErr, &rr) {
			out = restartRequiredOutcome(&res, configreload.StageCompile, rr)
			endStage(string(res.Category))
			return out
		}
		switch {
		case r.canceled(compileErr):
			out = cancelOut()
		case errors.Is(compileErr, errCompilePanic):
			out = failOutcome(&res, sdkreload.ResultInternalFailed, configreload.StagePanic)
		default:
			out = failOutcome(&res, sdkreload.ResultPreparationFailed, configreload.StageCompile)
		}
		endStage(string(out.Result.Category))
		return out
	}
	endStage("ok")
	if plane == nil {
		out = failOutcome(&res, sdkreload.ResultPreparationFailed, configreload.StageCompile)
		return out
	}

	if r.isShuttingDown() || ctx.Err() != nil {
		closeOwnedPlane(&plane)
		out = cancelOut()
		return out
	}

	_, endStage = r.beginStage(ctx, configreload.StagePrepare)
	label := string(in.Trigger.Kind)
	if label == "" {
		label = "reload"
	}
	gen = r.mgr.PrepareRequestPlane(label, plane)
	plane = nil // ownership transferred to gen
	gen.SetMetaHints(MetaHints{
		PublicFingerprint: eff.Identity.PublicFingerprint,
		TriggerKind:       string(in.Trigger.Kind),
		LoadedAt:          eff.LoadedAt,
	})
	endStage("ok")

	if r.isShuttingDown() {
		discardOwnedGeneration(&gen)
		out = cancelOut()
		return out
	}

	_, endStage = r.beginStage(ctx, configreload.StagePublish)
	publishErr := r.mgr.Publish(gen)
	var publishedGenID int64
	if publishErr == nil {
		publishedGenID = gen.ID()
	}
	gen = nil // Manager owns cleanup either way
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
	out = attemptOutcome{Result: res, EffectiveUpdate: eff, SourceUpdate: srcUpdate}
	return out
}

func closeOwnedPlane(owned *PublishedRequestPlane) {
	if owned == nil {
		return
	}
	p := *owned
	*owned = nil
	if p == nil {
		return
	}
	defer recoverPanic("plane close")
	_ = p.Close()
}

func discardOwnedGeneration(owned **Generation) {
	if owned == nil {
		return
	}
	g := *owned
	*owned = nil
	if g == nil {
		return
	}
	defer recoverPanic("generation discard")
	_ = g.Discard()
}

func recoverPanic(_ string) {
	if recovered := recover(); recovered != nil {
		_ = configreload.SanitizePanicValue(recovered)
	}
}

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
