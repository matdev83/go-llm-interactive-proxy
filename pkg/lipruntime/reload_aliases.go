package lipruntime

import (
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

// Public reload names are exact aliases / re-exports of the canonical
// pkg/lipsdk/configreload contract (Requirement 7.4). No mirrored domain copy.

type TriggerKind = sdkreload.TriggerKind
type ResultCategory = sdkreload.ResultCategory
type ReloadTrigger = sdkreload.Trigger
type ReloadResult = sdkreload.Result
type HistoryEntry = sdkreload.HistoryEntry
type ReloadStatus = sdkreload.Status

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

// AllResultCategories re-exports the canonical closed vocabulary for compatibility
// enumeration. Policy must not depend on mutating this slice.
var AllResultCategories = sdkreload.AllResultCategories
