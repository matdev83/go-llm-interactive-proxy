package compatibleparity

import (
	"encoding/json"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Family identifies one essential/generic protocol pair under test.
type Family string

const (
	FamilyOpenAILegacy    Family = "openai-legacy"
	FamilyOpenAIResponses Family = "openai-responses"
	FamilyAnthropic       Family = "anthropic"
)

// Scenario is one deterministic parity cell.
type Scenario string

const (
	ScenarioText               Scenario = "text"
	ScenarioReasoning          Scenario = "reasoning"
	ScenarioTools              Scenario = "tools"
	ScenarioMultimodal         Scenario = "multimodal"
	ScenarioUsage              Scenario = "usage"
	ScenarioError              Scenario = "error"
	ScenarioPostOutputError    Scenario = "post_output_error"
	ScenarioNoRetryAfterOutput Scenario = "no_retry_after_output"
	ScenarioCancellation       Scenario = "cancellation"
	ScenarioTerminalOrdering   Scenario = "terminal_ordering"
)

// Fixture is one canonical request/expectation pair shared by essential and
// generic adapters of a family.
type Fixture struct {
	Name                string
	Family              Family
	Scenario            Scenario
	Delivery            lipapi.DeliveryMode
	Call                lipapi.Call
	Model               string
	WantKinds           []lipapi.EventKind
	WantText            string
	WantTool            string
	WantToolArgs        string
	WantReason          string
	WantSignature       string
	WantFinishReason    string
	WantInputTokens     int
	WantOutputTokens    int
	WantTotalTokens     int
	WantReasoningTokens int
	// OpenFails means Open or the first Recv must fail (errors / cancellation).
	OpenFails bool
	// CancelAfterOpen cancels the stream context after Open returns.
	CancelAfterOpen bool
}

// AllFamilies returns the three compatible-mode protocol families.
func AllFamilies() []Family {
	return []Family{FamilyOpenAILegacy, FamilyOpenAIResponses, FamilyAnthropic}
}

// AllScenarios returns the Task 4.4 parity coverage matrix.
func AllScenarios() []Scenario {
	return []Scenario{
		ScenarioText,
		ScenarioReasoning,
		ScenarioTools,
		ScenarioMultimodal,
		ScenarioUsage,
		ScenarioError,
		ScenarioPostOutputError,
		ScenarioNoRetryAfterOutput,
		ScenarioCancellation,
		ScenarioTerminalOrdering,
	}
}

// ParityFixtures returns deterministic fixtures for every family × scenario cell,
// plus non-streaming variants for OpenAI families on success scenarios.
func ParityFixtures() []Fixture {
	out := make([]Fixture, 0, len(AllFamilies())*len(AllScenarios())*2)
	for _, family := range AllFamilies() {
		for _, scenario := range AllScenarios() {
			base := fixtureFor(family, scenario)
			out = append(out, base)
			if supportsNonStreaming(family, scenario) {
				out = append(out, base.WithDelivery(lipapi.DeliveryModeNonStreaming))
			}
		}
	}
	return out
}

func supportsNonStreaming(family Family, scenario Scenario) bool {
	if family == FamilyAnthropic {
		return false
	}
	switch scenario {
	case ScenarioError, ScenarioPostOutputError, ScenarioNoRetryAfterOutput, ScenarioCancellation:
		return true
	default:
		return true
	}
}

func fixtureFor(family Family, scenario Scenario) Fixture {
	model := modelFor(family)
	op := operationFor(family)
	base := Fixture{
		Name:     string(family) + "/" + string(scenario),
		Family:   family,
		Scenario: scenario,
		Delivery: lipapi.DeliveryModeStreaming,
		Model:    model,
		Call:     baseCall(op, scenario),
	}
	switch scenario {
	case ScenarioText:
		base.WantKinds = []lipapi.EventKind{
			lipapi.EventResponseStarted,
			lipapi.EventMessageStarted,
			lipapi.EventTextDelta,
			lipapi.EventResponseFinished,
		}
		base.WantText = "parity-text-ok"
	case ScenarioReasoning:
		base.WantKinds = []lipapi.EventKind{
			lipapi.EventResponseStarted,
			lipapi.EventMessageStarted,
			lipapi.EventReasoningDelta,
			lipapi.EventTextDelta,
			lipapi.EventResponseFinished,
		}
		if family == FamilyOpenAIResponses {
			// Responses maps completed reasoning items to EventReasoningPart.
			base.WantKinds = []lipapi.EventKind{
				lipapi.EventResponseStarted,
				lipapi.EventMessageStarted,
				lipapi.EventReasoningPart,
				lipapi.EventTextDelta,
				lipapi.EventResponseFinished,
			}
		}
		base.WantReason = "parity-think"
		base.WantText = "parity-reasoned-ok"
		if family == FamilyAnthropic {
			base.WantSignature = "sig"
		}
	case ScenarioTools:
		base.WantKinds = []lipapi.EventKind{
			lipapi.EventResponseStarted,
			lipapi.EventMessageStarted,
			lipapi.EventToolCallStarted,
			lipapi.EventToolCallArgsDelta,
			lipapi.EventToolCallFinished,
			lipapi.EventResponseFinished,
		}
		base.WantTool = "get_weather"
		base.WantToolArgs = "NYC"
		base.WantFinishReason = finishReasonForTools(family)
		base.Call.Tools = []lipapi.ToolDef{{
			Name:        "get_weather",
			Description: "weather",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		}}
	case ScenarioMultimodal:
		base.WantKinds = []lipapi.EventKind{
			lipapi.EventResponseStarted,
			lipapi.EventMessageStarted,
			lipapi.EventTextDelta,
			lipapi.EventResponseFinished,
		}
		base.WantText = "parity-vision-ok"
		base.Call.Messages = []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.TextPart("describe"),
				{Kind: lipapi.PartImageRef, ImageRef: "https://cdn.example/parity.png", ImageMIME: "image/png"},
			},
		}}
	case ScenarioUsage:
		base.WantKinds = []lipapi.EventKind{
			lipapi.EventResponseStarted,
			lipapi.EventMessageStarted,
			lipapi.EventTextDelta,
			lipapi.EventUsageDelta,
			lipapi.EventResponseFinished,
		}
		base.WantText = "parity-usage-ok"
		base.WantOutputTokens = 2
		base.WantTotalTokens = 3
		switch family {
		case FamilyAnthropic:
			// Anthropic usage is surfaced on message_delta; input tokens may be zero at the canonical seam.
			base.WantInputTokens = 0
			base.WantTotalTokens = 2
		case FamilyOpenAIResponses:
			base.WantInputTokens = 1
		default:
			base.WantInputTokens = 1
			base.WantReasoningTokens = 4
		}
		base.WantFinishReason = finishReasonStop(family)
	case ScenarioError:
		base.OpenFails = true
	case ScenarioPostOutputError:
		base.OpenFails = true
		base.WantText = "parity-partial-ok"
	case ScenarioNoRetryAfterOutput:
		base.OpenFails = true
		base.WantText = "parity-partial-ok"
	case ScenarioCancellation:
		base.OpenFails = true
		base.CancelAfterOpen = true
	case ScenarioTerminalOrdering:
		// Terminal outcome must be last among lifecycle events; usage may precede finish.
		base.WantKinds = []lipapi.EventKind{
			lipapi.EventResponseStarted,
			lipapi.EventMessageStarted,
			lipapi.EventTextDelta,
			lipapi.EventUsageDelta,
			lipapi.EventResponseFinished,
		}
		base.WantText = "parity-terminal-ok"
		base.WantOutputTokens = 2
		base.WantTotalTokens = 3
		switch family {
		case FamilyAnthropic:
			base.WantInputTokens = 0
			base.WantTotalTokens = 2
		case FamilyOpenAIResponses:
			base.WantInputTokens = 1
		default:
			base.WantInputTokens = 1
			base.WantReasoningTokens = 4
		}
		base.WantFinishReason = finishReasonStop(family)
	}
	return base
}

func finishReasonStop(family Family) string {
	if family == FamilyAnthropic {
		return "end_turn"
	}
	return "stop"
}

func finishReasonForTools(family Family) string {
	if family == FamilyAnthropic {
		return "tool_use"
	}
	if family == FamilyOpenAIResponses {
		return ""
	}
	return "tool_calls"
}

func modelFor(family Family) string {
	switch family {
	case FamilyAnthropic:
		return "claude-parity-test"
	default:
		return "gpt-parity-test"
	}
}

func operationFor(family Family) lipapi.Operation {
	switch family {
	case FamilyOpenAIResponses:
		return lipapi.OperationOpenAIResponses
	case FamilyAnthropic:
		// Anthropic Messages has no separate lipapi.Operation constant; adapters
		// key off the backend family rather than Invocation.Operation.
		return ""
	default:
		return lipapi.OperationOpenAIChatCompletions
	}
}

func baseCall(op lipapi.Operation, scenario Scenario) lipapi.Call {
	call := lipapi.Call{
		ID: "compatible-parity",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Invocation: lipapi.Invocation{
			Operation: op,
			// Leave TransportMode empty so essential (stream-first) and
			// openaicompat (empty => streaming) share one wire dialect.
			DeliveryMode: lipapi.DeliveryModeStreaming,
		},
	}
	_ = scenario
	return call
}
