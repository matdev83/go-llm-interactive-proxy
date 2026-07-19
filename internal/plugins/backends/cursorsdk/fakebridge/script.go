package fakebridge

import "encoding/json"

// Action names recognized by the fake bridge script runner.
const (
	ActionRespond          = "respond"
	ActionEvent            = "event"
	ActionStderr           = "stderr"
	ActionMalformed        = "malformed"
	ActionOversized        = "oversized"
	ActionExit             = "exit"
	ActionBlockCancel      = "block_cancel"
	ActionIgnoreShutdown   = "ignore_shutdown"
	ActionOutOfOrderEvents = "out_of_order_events"
	ActionSleep            = "sleep"
	ActionWaitForFile      = "wait_for_file"
	ActionHoldUntilCancel  = "hold_until_cancel"
)

// Script is a deterministic sequence of fake-bridge actions keyed by method or
// special triggers.
type Script struct {
	ImplVersion          string          `json:"implVersion"`
	SDKVersion           string          `json:"sdkVersion"`
	Generation           int             `json:"generation"`
	Models               json.RawMessage `json:"models"`
	SandboxSupported     *bool           `json:"sandboxSupported"`
	OmitSandboxSupported bool            `json:"omitSandboxSupported"`
	CreateCountFile      string          `json:"createCountFile,omitempty"`
	// PromptCaptureFile, when set, receives the latest agent/send prompt bytes (test proof only).
	PromptCaptureFile string              `json:"promptCaptureFile,omitempty"`
	OnMethod          map[string][]Action `json:"onMethod"`
	// OnAgentSend is optional per-send action lists (1st send → index 0, …).
	// When set for a send index, it overrides OnMethod[agent/send] for that send.
	OnAgentSend [][]Action `json:"onAgentSend,omitempty"`
	OnStartup   []Action   `json:"onStartup"`
}

// AutoRunID in Action.Result runId or Action.RunID allocates a dynamic run-N id.
const AutoRunID = "$auto"

// Action is one scripted bridge behavior.
type Action struct {
	Type    string          `json:"type"`
	Method  string          `json:"method,omitempty"`
	IDFrom  string          `json:"idFrom,omitempty"` // "request" echoes request id
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
	RunID   string          `json:"runId,omitempty"`
	Seq     int64           `json:"seq,omitempty"`
	Kind    string          `json:"kind,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Text    string          `json:"text,omitempty"`
	Code    int             `json:"code,omitempty"`
	Bytes   int             `json:"bytes,omitempty"`
	Line    string          `json:"line,omitempty"`
	Ms      int             `json:"ms,omitempty"`   // ActionSleep duration / ActionWaitForFile timeout
	Path    string          `json:"path,omitempty"` // ActionWaitForFile gate / HoldUntilCancel active-notify path
}

func DefaultScript() Script {
	models := json.RawMessage(`[{"id":"gpt-5.3-codex","displayName":"GPT-5.3 Codex","parameters":[{"id":"reasoning","values":["low","medium","high","xhigh"]}]}]`)
	return Script{
		ImplVersion: "fake-1.0.0",
		SDKVersion:  "1.0.23",
		Generation:  1,
		Models:      models,
		OnMethod:    map[string][]Action{},
	}
}
