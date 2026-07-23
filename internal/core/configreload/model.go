package configreload

import (
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

// Canonical reload vocabulary lives in pkg/lipsdk/configreload.
// This package retains type aliases for transitional internal call sites and owns
// algorithms (history ring, sanitization, reloadability policy) that are not part
// of the public contract. Trigger/result constants and AllResultCategories are not
// redeclared here — import pkg/lipsdk/configreload for the closed vocabulary values.

type TriggerKind = sdkreload.TriggerKind
type ResultCategory = sdkreload.ResultCategory
type Trigger = sdkreload.Trigger
type Result = sdkreload.Result
type Status = sdkreload.Status
type HistoryEntry = sdkreload.HistoryEntry

// Compatibility aliases preserve existing internal names during migration.
type ReloadTrigger = sdkreload.Trigger
type ReloadResult = sdkreload.Result
type ReloadStatus = sdkreload.Status

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

// Transitional constant aliases — these are thin re-exports, not a second vocabulary.
// Architecture gates exempt only direct type aliases that select the approved
// canonical target through a pkg/lipsdk/configreload import; value aliases remain
// until remaining public/HTTP/cmd call sites import pkg/lipsdk/configreload directly
// (Task 2.3). Orchestration/observability already consumes the canonical package.
const (
	TriggerSIGHUP = sdkreload.TriggerSIGHUP
	TriggerAPI    = sdkreload.TriggerAPI

	ResultPublished         = sdkreload.ResultPublished
	ResultNoop              = sdkreload.ResultNoop
	ResultBusy              = sdkreload.ResultBusy
	ResultRestartRequired   = sdkreload.ResultRestartRequired
	ResultRetentionBlocked  = sdkreload.ResultRetentionBlocked
	ResultInvalid           = sdkreload.ResultInvalid
	ResultSourceIntegrity   = sdkreload.ResultSourceIntegrity
	ResultCanceled          = sdkreload.ResultCanceled
	ResultPreparationFailed = sdkreload.ResultPreparationFailed
	ResultInternalFailed    = sdkreload.ResultInternalFailed
)

// AllResultCategories aliases the canonical closed vocabulary slice header.
var AllResultCategories = sdkreload.AllResultCategories
