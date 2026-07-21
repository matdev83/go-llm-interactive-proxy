package configreload

import (
	"time"
)

// TriggerKind identifies the explicit reload trigger surface (req 1.2-1.4, 11.x).
// Triggers never carry paths, YAML, URLs, or plugin-install instructions.
type TriggerKind string

const (
	TriggerSIGHUP TriggerKind = "sighup"
	TriggerAPI    TriggerKind = "api"
)

// ReloadTrigger is the host-accepted explicit reload envelope (design Reload Coordinator).
type ReloadTrigger struct {
	Kind       TriggerKind
	AcceptedAt time.Time
	SafeActor  string
}

// ResultCategory is the terminal reload attempt classification (req 3.10).
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

// AllResultCategories is the closed vocabulary for terminal attempt results (req 3.10).
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

// ReloadResult is the bounded, secret-safe terminal outcome of one attempt (req 3.10, 12.8, 14.1).
// It never carries raw YAML, private digests, credentials, or configuration values.
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

// ReloadStatus is the bounded public status snapshot (req 13.1-13.2, 14.1).
type ReloadStatus struct {
	ActiveGeneration int64
	LastResult       ReloadResult
	Busy             bool
	FixedSourcePath  string
	PendingSignal    bool
	CoalescedSignals int64
}

// Stage names used in ReasonCategory / diagnostics (bounded, non-secret).
const (
	StageRead      = "read"
	StageLoad      = "load"
	StageNoop      = "noop"
	StageClassify  = "classify"
	StageCompile   = "compile"
	StagePrepare   = "prepare"
	StageRetention = "retention"
	StagePublish   = "publish"
	StageRollback  = "rollback"
	StageShutdown  = "shutdown"
	StageBusy      = "busy"
	StageCoalesce  = "coalesce"
	StagePanic     = "panic"
)
