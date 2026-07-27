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

type reloadStateInitial struct {
	ActiveEffective *config.EffectiveConfig
	ActiveSource    *configsource.ActiveSourceVersion
	InitialResult   sdkreload.Result
	ModelGeneration string
	HistoryCapacity int
}

type reloadTerminalMeta struct {
	Trigger    sdkreload.Trigger
	Duration   time.Duration
	RecordedAt time.Time
}

type reloadStatusInput struct {
	ActiveGeneration    int64
	Busy, PendingSignal bool
	CoalescedSignals    int64
	RetainedGenerations int
	RetentionPressure   bool
}

// ReloadState owns active effective/source snapshot, last result/success/failure,
// source-integrity posture, model-generation fingerprint, bounded history, and
// canonical status composition (req 6.3-6.4, 7.1-7.8).
type ReloadState struct {
	mu                             sync.Mutex
	activeEff                      *config.EffectiveConfig
	activeSource                   *configsource.ActiveSourceVersion
	last, lastSuccess, lastFailure sdkreload.Result
	sourcePosture, modelGen        string
	historyCap                     int
	history                        []sdkreload.HistoryEntry
}

func newReloadState(in reloadStateInitial) *ReloadState {
	cap := in.HistoryCapacity
	if cap <= 0 {
		cap = configreload.DefaultStatusHistoryCap
	}
	s := &ReloadState{
		activeEff: in.ActiveEffective, activeSource: cloneActiveSource(in.ActiveSource),
		sourcePosture: "ok", modelGen: in.ModelGeneration, historyCap: cap,
	}
	if in.InitialResult.Category != "" {
		s.last = in.InitialResult.Clone()
		s.lastSuccess = s.last
	}
	return s
}

func cloneActiveSource(in *configsource.ActiveSourceVersion) *configsource.ActiveSourceVersion {
	if in == nil {
		return nil
	}
	cp := *in
	return &cp
}

func (s *ReloadState) ActiveInput(trigger sdkreload.Trigger, attemptID, activeGeneration int64) attemptInput {
	if s == nil {
		return attemptInput{Trigger: trigger, AttemptID: attemptID, ActiveGeneration: activeGeneration}
	}
	s.mu.Lock()
	eff, src := s.activeEff, cloneActiveSource(s.activeSource)
	s.mu.Unlock()
	return attemptInput{
		Trigger: trigger, AttemptID: attemptID, ActiveGeneration: activeGeneration,
		ActiveEffective: eff, ActiveSource: src,
	}
}

// Apply applies one completed attempt outcome and terminal metadata. Busy or
// empty-category results return a defensive clone without mutating state.
func (s *ReloadState) Apply(outcome attemptOutcome, meta reloadTerminalMeta) sdkreload.Result {
	if s == nil {
		return outcome.Result.Clone()
	}
	res := outcome.Result.Clone()
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
		s.lastSuccess, s.sourcePosture = res, "ok"
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
		s.lastFailure, s.sourcePosture = res, "ok" // includes source-baseline no-op (req 2.9)
		if outcome.SourceUpdate != nil {
			s.activeSource = cloneActiveSource(outcome.SourceUpdate)
		}
	case sdkreload.ResultSourceIntegrity:
		s.lastFailure, s.sourcePosture = res, "failed"
	default:
		s.lastFailure = res
	}
	s.appendHistoryLocked(res, meta.Trigger, meta.Duration, recordedAt)
	s.mu.Unlock()
	return res.Clone()
}

func (s *ReloadState) appendHistoryLocked(res sdkreload.Result, trigger sdkreload.Trigger, d time.Duration, recordedAt time.Time) {
	if s.historyCap <= 0 {
		return
	}
	stage := res.ReasonCategory
	if stage == "" {
		stage = string(res.Category)
	}
	entry := sdkreload.HistoryEntry{
		AttemptID: res.AttemptID, Trigger: trigger.Kind, Stage: boundStageName(stage),
		Category: res.Category, ActiveGeneration: res.ActiveGeneration,
		CandidateGeneration: candidateGeneration(res), DurationMs: d.Milliseconds(),
		RestartFieldCount: res.RestartFieldCount, ReasonCategory: sanitizeHistoryReason(res.ReasonCategory),
		SafeActor: sanitizeHistoryActor(trigger.SafeActor), RecordedAt: recordedAt,
	}
	if len(s.history) < s.historyCap {
		s.history = append(s.history, entry)
		return
	}
	copy(s.history, s.history[1:])
	s.history[len(s.history)-1] = entry
}

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
		ActiveGeneration: in.ActiveGeneration, CurrentAttempt: current,
		LastResult: s.last, LastSuccess: s.lastSuccess, LastFailure: s.lastFailure,
		SourceIntegrity: posture, RetainedGenerations: in.RetainedGenerations,
		RetentionPressure: in.RetentionPressure, ControlDegraded: controlDegraded,
		ModelGeneration: s.modelGen, History: s.history, Busy: in.Busy,
		PendingSignal: in.PendingSignal, CoalescedSignals: in.CoalescedSignals,
	}.Clone()
}

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
	for _, sub := range []string{"password", "secret", "api_key", "apikey", "token", "bearer", "sk-"} {
		if strings.Contains(low, sub) {
			return true
		}
	}
	return false
}
