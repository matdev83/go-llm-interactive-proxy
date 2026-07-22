package lipruntime

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
)

func mapTriggerIn(t ReloadTrigger) (configreload.ReloadTrigger, bool) {
	var kind configreload.TriggerKind
	switch t.Kind {
	case TriggerAPI:
		kind = configreload.TriggerAPI
	case TriggerSIGHUP:
		kind = configreload.TriggerSIGHUP
	case "":
		return configreload.ReloadTrigger{}, false
	default:
		return configreload.ReloadTrigger{}, false
	}
	return configreload.ReloadTrigger{
		Kind:       kind,
		AcceptedAt: t.AcceptedAt,
		SafeActor:  t.SafeActor,
	}, true
}

func mapCategoryOut(c configreload.ResultCategory) ResultCategory {
	switch c {
	case configreload.ResultPublished:
		return ResultPublished
	case configreload.ResultNoop:
		return ResultNoop
	case configreload.ResultBusy:
		return ResultBusy
	case configreload.ResultRestartRequired:
		return ResultRestartRequired
	case configreload.ResultRetentionBlocked:
		return ResultRetentionBlocked
	case configreload.ResultInvalid:
		return ResultInvalid
	case configreload.ResultSourceIntegrity:
		return ResultSourceIntegrity
	case configreload.ResultCanceled:
		return ResultCanceled
	case configreload.ResultPreparationFailed:
		return ResultPreparationFailed
	case configreload.ResultInternalFailed:
		return ResultInternalFailed
	case "":
		return ""
	default:
		return ResultInternalFailed
	}
}

func mapTriggerOut(k configreload.TriggerKind) TriggerKind {
	switch k {
	case configreload.TriggerAPI:
		return TriggerAPI
	case configreload.TriggerSIGHUP:
		return TriggerSIGHUP
	default:
		return TriggerKind(k)
	}
}

func mapResultOut(in configreload.ReloadResult) ReloadResult {
	out := ReloadResult{
		Category:           mapCategoryOut(in.Category),
		AttemptID:          in.AttemptID,
		ActiveGeneration:   in.ActiveGeneration,
		PreviousGeneration: in.PreviousGeneration,
		RestartFieldCount:  in.RestartFieldCount,
		ReasonCategory:     in.ReasonCategory,
		CoalescedSignals:   in.CoalescedSignals,
	}
	if in.RestartFields != nil {
		out.RestartFields = append([]string(nil), in.RestartFields...)
	}
	return out
}

func mapHistoryOut(in []configreload.HistoryEntry) []HistoryEntry {
	if in == nil {
		return nil
	}
	out := make([]HistoryEntry, len(in))
	for i := range in {
		out[i] = HistoryEntry{
			AttemptID:           in[i].AttemptID,
			Trigger:             mapTriggerOut(in[i].Trigger),
			Stage:               in[i].Stage,
			Category:            mapCategoryOut(in[i].Category),
			ActiveGeneration:    in[i].ActiveGeneration,
			CandidateGeneration: in[i].CandidateGeneration,
			DurationMs:          in[i].DurationMs,
			RestartFieldCount:   in[i].RestartFieldCount,
			ReasonCategory:      in[i].ReasonCategory,
			SafeActor:           in[i].SafeActor,
			RecordedAt:          in[i].RecordedAt,
		}
	}
	return out
}

func mapStatusOut(in configreload.ReloadStatus) ReloadStatus {
	out := ReloadStatus{
		ActiveGeneration:    in.ActiveGeneration,
		LastResult:          mapResultOut(in.LastResult),
		LastSuccess:         mapResultOut(in.LastSuccess),
		LastFailure:         mapResultOut(in.LastFailure),
		SourceIntegrity:     in.SourceIntegrity,
		RetainedGenerations: in.RetainedGenerations,
		RetentionPressure:   in.RetentionPressure,
		ControlDegraded:     in.ControlDegraded,
		ModelGeneration:     in.ModelGeneration,
		History:             mapHistoryOut(in.History),
		Busy:                in.Busy,
		PendingSignal:       in.PendingSignal,
		CoalescedSignals:    in.CoalescedSignals,
		// FixedSourcePath intentionally omitted: public facade must not expose paths.
	}
	if in.CurrentAttempt != nil {
		cur := mapResultOut(*in.CurrentAttempt)
		out.CurrentAttempt = &cur
	}
	return out
}
