package runtimehost

import (
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

// reloadStateInitial seeds ReloadState from CoordinatorDeps plus Manager
// active metadata gathered once by Coordinator at construction (req 6.3).
// ReloadState itself never reads Manager.
type reloadStateInitial struct {
	ActiveEffective *config.EffectiveConfig
	ActiveSource    *configsource.ActiveSourceVersion
	InitialResult   sdkreload.Result
	ModelGeneration string
	HistoryCapacity int
}

// reloadTerminalMeta is host-supplied timing/trigger metadata for one Apply
// transaction. ReloadState never calls an external clock while locked except
// the trivial RecordedAt fallback below.
type reloadTerminalMeta struct {
	Trigger    sdkreload.Trigger
	Duration   time.Duration
	RecordedAt time.Time
}

// reloadStatusInput carries the dynamic gate/manager primitives Coordinator
// gathers outside ReloadState's lock before calling Snapshot.
type reloadStatusInput struct {
	ActiveGeneration    int64
	Busy                bool
	PendingSignal       bool
	CoalescedSignals    int64
	RetainedGenerations int
	RetentionPressure   bool
}

// ReloadState exclusively owns the active effective/source snapshot, last
// result/success/failure, source-integrity posture, safe model-generation
// fingerprint, and bounded canonical history, and composes the complete
// secret-safe canonical status snapshot (req 6.3-6.4, 7.1-7.8). It never
// depends on or stores Manager, Source, Loader, Compiler, AttemptRunner,
// AttemptGate, Coordinator, ReloadObserver, HTTP, logging, tracing, or
// metrics, and it never compiles or publishes. One private lock guards all
// state below.
type ReloadState struct {
	mu sync.Mutex

	activeEff    *config.EffectiveConfig
	activeSource *configsource.ActiveSourceVersion

	last          sdkreload.Result
	lastSuccess   sdkreload.Result
	lastFailure   sdkreload.Result
	sourcePosture string
	modelGen      string

	historyCap int
	history    []sdkreload.HistoryEntry
}

// newReloadState constructs the sole production ReloadState. The initial
// active generation publishes LastResult/LastSuccess and the public model
// fingerprint but never invents a reload history attempt (req 6.3).
func newReloadState(in reloadStateInitial) *ReloadState {
	cap := in.HistoryCapacity
	if cap <= 0 {
		cap = configreload.DefaultStatusHistoryCap
	}
	s := &ReloadState{
		activeEff:     in.ActiveEffective,
		activeSource:  cloneActiveSource(in.ActiveSource),
		sourcePosture: "ok",
		modelGen:      in.ModelGeneration,
		historyCap:    cap,
	}
	if in.InitialResult.Category != "" {
		s.last = in.InitialResult.Clone()
		s.lastSuccess = s.last
	}
	return s
}

// ActiveInput returns an immutable attemptInput snapshot for one admitted
// attempt transaction, cloning the mutable active source at this boundary
// (req 6.2, 6.10-6.11).
func (s *ReloadState) ActiveInput(trigger sdkreload.Trigger, attemptID, activeGeneration int64) attemptInput {
	if s == nil {
		return attemptInput{Trigger: trigger, AttemptID: attemptID, ActiveGeneration: activeGeneration}
	}
	s.mu.Lock()
	eff := s.activeEff
	src := cloneActiveSource(s.activeSource)
	s.mu.Unlock()
	return attemptInput{
		Trigger:          trigger,
		AttemptID:        attemptID,
		ActiveGeneration: activeGeneration,
		ActiveEffective:  eff,
		ActiveSource:     src,
	}
}

// Apply atomically applies one completed admitted attempt's outcome and
// terminal metadata: it updates active effective/source (when the outcome
// carries them), last/last-success/last-failure, source-integrity posture,
// safe model-generation fingerprint, and appends exactly one bounded history
// entry. Callers must never invoke Apply for a Busy or empty-category result;
// those non-terminal categories return a defensive clone without mutating
// active/terminal/history state. Unknown non-empty categories normalize once
// to internal-failed and are applied. It returns a defensive copy of the
// canonical terminal result (or the non-terminal clone).
func (s *ReloadState) Apply(outcome attemptOutcome, meta reloadTerminalMeta) sdkreload.Result {
	if s == nil {
		return outcome.Result.Clone()
	}
	res := outcome.Result.Clone()
	// Non-terminal outcomes must not touch active/terminal/history state.
	if res.Category == sdkreload.ResultBusy || res.Category == "" {
		return res
	}
	res.Category = sdkreload.NormalizeResultCategory(res.Category)
	recordedAt := meta.RecordedAt
	if recordedAt.IsZero() {
		recordedAt = time.Now().UTC()
	}

	s.mu.Lock()
	s.last = res
	switch res.Category {
	case sdkreload.ResultPublished:
		s.lastSuccess = res
		s.sourcePosture = "ok"
		if outcome.EffectiveUpdate != nil {
			s.activeEff = outcome.EffectiveUpdate
			if fp := outcome.EffectiveUpdate.Identity.PublicFingerprint; fp != "" {
				s.modelGen = fp
			}
		}
		if outcome.SourceUpdate != nil {
			s.activeSource = cloneActiveSource(outcome.SourceUpdate)
		}
	case sdkreload.ResultNoop:
		// Covers both the plain atomic no-op (no updates) and the
		// effective-identity no-op that must still advance the source
		// baseline (SourceUpdate only, req 2.9).
		s.lastFailure = res
		s.sourcePosture = "ok"
		if outcome.SourceUpdate != nil {
			s.activeSource = cloneActiveSource(outcome.SourceUpdate)
		}
	case sdkreload.ResultSourceIntegrity:
		s.lastFailure = res
		s.sourcePosture = "failed"
	default:
		s.lastFailure = res
	}
	s.appendHistoryLocked(res, meta.Trigger, meta.Duration, recordedAt)
	s.mu.Unlock()
	return res.Clone()
}

// appendHistoryLocked appends one bounded, secret-safe history entry. Caller
// must hold s.mu. Candidate generation is the active generation only for
// published outcomes (preserves prior observer candidateGeneration policy).
func (s *ReloadState) appendHistoryLocked(res sdkreload.Result, trigger sdkreload.Trigger, d time.Duration, recordedAt time.Time) {
	if s.historyCap <= 0 {
		return
	}
	stage := res.ReasonCategory
	if stage == "" {
		stage = string(res.Category)
	}
	entry := sdkreload.HistoryEntry{
		AttemptID:           res.AttemptID,
		Trigger:             trigger.Kind,
		Stage:               boundStageName(stage),
		Category:            res.Category,
		ActiveGeneration:    res.ActiveGeneration,
		CandidateGeneration: candidateGeneration(res),
		DurationMs:          d.Milliseconds(),
		RestartFieldCount:   res.RestartFieldCount,
		ReasonCategory:      sanitizeHistoryReason(res.ReasonCategory),
		SafeActor:           sanitizeHistoryActor(trigger.SafeActor),
		RecordedAt:          recordedAt,
	}
	if len(s.history) < s.historyCap {
		s.history = append(s.history, entry)
		return
	}
	// Bounded ring: drop oldest, append newest. historyCap is small
	// (default 32) so a shift is simpler and safer than a ring index and
	// is not a hot path (one call per completed reload attempt).
	copy(s.history, s.history[1:])
	s.history[len(s.history)-1] = entry
}

// Snapshot composes the complete secret-safe canonical Status from ReloadState
// primitives plus the dynamic gate/manager input Coordinator gathers outside
// any lock. The returned Status is a defensive copy (CurrentAttempt/History/
// RestartFields never alias ReloadState-owned storage).
func (s *ReloadState) Snapshot(in reloadStatusInput) sdkreload.Status {
	if s == nil {
		return sdkreload.Status{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var current *sdkreload.Result
	if in.Busy {
		cur := s.last
		current = &cur
	}
	controlDegraded := s.lastFailure.Category != "" &&
		s.lastFailure.Category != sdkreload.ResultPublished &&
		s.lastFailure.Category != sdkreload.ResultNoop
	posture := s.sourcePosture
	if posture == "" {
		posture = "unknown"
	}
	return sdkreload.Status{
		ActiveGeneration:    in.ActiveGeneration,
		CurrentAttempt:      current,
		LastResult:          s.last,
		LastSuccess:         s.lastSuccess,
		LastFailure:         s.lastFailure,
		SourceIntegrity:     posture,
		RetainedGenerations: in.RetainedGenerations,
		RetentionPressure:   in.RetentionPressure,
		ControlDegraded:     controlDegraded,
		ModelGeneration:     s.modelGen,
		History:             s.history,
		Busy:                in.Busy,
		PendingSignal:       in.PendingSignal,
		CoalescedSignals:    in.CoalescedSignals,
	}.Clone()
}

// sanitizeHistoryActor matches prior configreload.StatusHistory truncateActor
// policy: trim, bound to 64 bytes, then secret-looking text → RedactedPlaceholder.
func sanitizeHistoryActor(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 64 {
		s = s[:64]
	}
	if looksLikeSecretText(s) {
		return configreload.RedactedPlaceholder
	}
	return s
}

// sanitizeHistoryReason matches prior configreload.StatusHistory sanitizeStage
// policy for ReasonCategory: trim, secret-looking text → "other", then bound
// to 64 bytes. Stage itself continues through boundStageName.
func sanitizeHistoryReason(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if looksLikeSecretText(s) {
		return "other"
	}
	if len(s) > 64 {
		return s[:64]
	}
	return s
}

func looksLikeSecretText(s string) bool {
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "password"),
		strings.Contains(low, "secret"),
		strings.Contains(low, "api_key"),
		strings.Contains(low, "apikey"),
		strings.Contains(low, "token"),
		strings.Contains(low, "bearer"),
		strings.Contains(low, "sk-"):
		return true
	default:
		return false
	}
}
