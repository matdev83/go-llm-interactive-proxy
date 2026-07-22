package lipruntime

import (
	"context"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
)

// TriggerKind identifies the explicit reload trigger surface.
// Triggers never carry paths, YAML, URLs, or plugin-install instructions.
type TriggerKind string

const (
	TriggerSIGHUP TriggerKind = "sighup"
	TriggerAPI    TriggerKind = "api"
)

// ReloadTrigger is the public explicit reload envelope.
type ReloadTrigger struct {
	Kind       TriggerKind
	AcceptedAt time.Time
	SafeActor  string
}

// ResultCategory is the terminal reload attempt classification.
type ResultCategory string

const (
	ResultPublished         ResultCategory = "published"
	ResultNoop              ResultCategory = "no-op"
	ResultBusy              ResultCategory = "busy"
	ResultRestartRequired   ResultCategory = "restart-required"
	ResultRetentionBlocked  ResultCategory = "retention-blocked"
	ResultInvalid           ResultCategory = "invalid"
	ResultSourceIntegrity   ResultCategory = "source-integrity-failed"
	ResultCanceled          ResultCategory = "canceled"
	ResultPreparationFailed ResultCategory = "preparation-failed"
	ResultInternalFailed    ResultCategory = "internal-failed"
)

// AllResultCategories is the closed vocabulary for terminal attempt results.
var AllResultCategories = []ResultCategory{
	ResultPublished,
	ResultNoop,
	ResultBusy,
	ResultRestartRequired,
	ResultRetentionBlocked,
	ResultInvalid,
	ResultSourceIntegrity,
	ResultCanceled,
	ResultPreparationFailed,
	ResultInternalFailed,
}

// ReloadResult is the bounded, secret-safe terminal outcome of one attempt.
// It never carries raw YAML, private digests, credentials, paths, or configuration values.
type ReloadResult struct {
	Category           ResultCategory
	AttemptID          int64
	ActiveGeneration   int64
	PreviousGeneration int64
	RestartFields      []string
	RestartFieldCount  int
	ReasonCategory     string
	CoalescedSignals   int64
}

// HistoryEntry is one bounded, secret-safe reload history record.
type HistoryEntry struct {
	AttemptID           int64
	Trigger             TriggerKind
	Stage               string
	Category            ResultCategory
	ActiveGeneration    int64
	CandidateGeneration int64
	DurationMs          int64
	RestartFieldCount   int
	ReasonCategory      string
	SafeActor           string
	RecordedAt          time.Time
}

// ReloadStatus is the bounded public status snapshot.
// It never carries raw YAML, credentials, DSNs, filesystem paths, or opaque plugin configuration.
type ReloadStatus struct {
	ActiveGeneration    int64
	CurrentAttempt      *ReloadResult
	LastResult          ReloadResult
	LastSuccess         ReloadResult
	LastFailure         ReloadResult
	SourceIntegrity     string
	RetainedGenerations int
	RetentionPressure   bool
	ControlDegraded     bool
	ModelGeneration     string
	History             []HistoryEntry
	Busy                bool
	PendingSignal       bool
	CoalescedSignals    int64
}

// reloadQuery is the narrow coordinator/query seam (satisfied by *runtimehost.Coordinator).
// It is unexported so public callers never see internal configreload types.
type reloadQuery interface {
	Reload(ctx context.Context, trigger configreload.ReloadTrigger) configreload.ReloadResult
	Status() configreload.ReloadStatus
}

// ReloadControl is the importable, thread-safe public reload and status facade.
// It delegates to the host coordinator/query seam and never duplicates reload logic.
type ReloadControl struct {
	q reloadQuery
}

func newReloadControl(q reloadQuery) *ReloadControl {
	if q == nil {
		return nil
	}
	return &ReloadControl{q: q}
}

// Reload runs one explicit reload attempt through the bound coordinator seam.
func (c *ReloadControl) Reload(ctx context.Context, trigger ReloadTrigger) ReloadResult {
	if c == nil || c.q == nil {
		return ReloadResult{Category: ResultInternalFailed, ReasonCategory: "reload-unavailable"}
	}
	if ctx == nil {
		return ReloadResult{Category: ResultInternalFailed, ReasonCategory: "nil-context"}
	}
	in, ok := mapTriggerIn(trigger)
	if !ok {
		return ReloadResult{Category: ResultInvalid, ReasonCategory: "trigger"}
	}
	return mapResultOut(c.q.Reload(ctx, in))
}

// Status returns a defensive copy of the safe reload status snapshot.
func (c *ReloadControl) Status() ReloadStatus {
	if c == nil || c.q == nil {
		return ReloadStatus{}
	}
	return mapStatusOut(c.q.Status())
}

// Reload runs one explicit reload attempt when a coordinator is bound to this runtime.
func (r *Runtime) Reload(ctx context.Context, trigger ReloadTrigger) ReloadResult {
	if r == nil || r.reload == nil {
		return ReloadResult{Category: ResultInternalFailed, ReasonCategory: "reload-unavailable"}
	}
	return r.reload.Reload(ctx, trigger)
}

// ReloadStatus returns the safe reload status snapshot for this runtime.
func (r *Runtime) ReloadStatus() ReloadStatus {
	if r == nil || r.reload == nil {
		return ReloadStatus{}
	}
	return r.reload.Status()
}

// ReloadControl returns the bound reload facade, or nil when reload is unavailable.
func (r *Runtime) ReloadControl() *ReloadControl {
	if r == nil {
		return nil
	}
	return r.reload
}

// bindReloadQuery attaches the host coordinator/query seam without exposing it.
func (r *Runtime) bindReloadQuery(q reloadQuery) {
	if r == nil {
		return
	}
	r.reload = newReloadControl(q)
}
