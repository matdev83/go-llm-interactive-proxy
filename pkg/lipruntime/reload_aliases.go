package lipruntime

import (
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

// Public reload names alias pkg/lipsdk/configreload (Req 7.4); no mirrored domain copy.

type (
	TriggerKind    = sdkreload.TriggerKind
	ResultCategory = sdkreload.ResultCategory
	ReloadTrigger  = sdkreload.Trigger
	ReloadResult   = sdkreload.Result
	HistoryEntry   = sdkreload.HistoryEntry
	ReloadStatus   = sdkreload.Status
)

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

// AllResultCategories re-exports the canonical closed vocabulary; do not mutate.
var AllResultCategories = sdkreload.AllResultCategories
