package runtime

import (
	"strings"
	"unicode/utf8"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

// continuationIntentFacts is the immutable, bounded carry-forward of an
// accepted continuation intent. References stay opaque to core; set preserves
// the distinction between an explicit empty control reference and legacy
// requests that never entered the continuation path.
type continuationIntentFacts struct {
	trajectoryRef string
	controlRef    string
	attempt       uint8
	set           bool
}

// projectTerminalDecisionEvidence copies the small canonical view that a
// terminal-decision provider may inspect. It deliberately reads request and
// response facts only; it has no policy, provider, or terminal ownership.
func projectTerminalDecisionEvidence(request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) terminaldecision.Evidence {
	items := lipapi.NormalizedItems(request.call)
	evidence := terminaldecision.Evidence{
		Objective:     boundedTerminalDecisionText(terminalDecisionObjective(items)),
		RecentText:    boundedTerminalDecisionText(terminalDecisionRecentText(items)),
		CandidateText: boundedTerminalDecisionText(terminalDecisionCandidateText(p)),
		ExplicitCompletion: lipapi.HasExplicitCompletion(
			terminalDecisionCompletionItems(items, p),
		),
	}

	trajectoryRef := strings.TrimSpace(request.continuationIntent.trajectoryRef)
	progressRef := strings.TrimSpace(request.continuationIntent.controlRef)
	attemptNumber := request.continuationIntent.attempt
	if !request.continuationIntent.set {
		trajectoryRef = strings.TrimSpace(request.call.ID)
		if trajectoryRef == "" {
			trajectoryRef = strings.TrimSpace(request.call.PreviousResponseID)
		}
		if trajectoryRef == "" {
			trajectoryRef = strings.TrimSpace(request.traceID)
		}
		progressRef = ""
		if attempt != nil {
			progressRef = strings.TrimSpace(attempt.bleg.BLegID)
		}
		if progressRef == "" {
			progressRef = trajectoryRef
		}
		attemptNumber = 0
		if attempt != nil && attempt.bleg.Seq > 0 {
			if attempt.bleg.Seq >= 1<<8 {
				attemptNumber = ^uint8(0)
			} else {
				attemptNumber = uint8(attempt.bleg.Seq)
			}
		}
	}
	evidence.Lineage = terminaldecision.EvidenceLineage{
		TrajectoryRef: boundedTerminalDecisionIdentifier(trajectoryRef),
		ParentRef:     boundedTerminalDecisionIdentifier(request.call.PreviousResponseID),
		ProgressRef:   boundedTerminalDecisionIdentifier(progressRef),
		Attempt:       attemptNumber,
	}

	actions := terminalDecisionActions(items, p)
	evidence.ActionCount = uint8(len(actions))
	copy(evidence.Actions[:], actions)
	return evidence
}

func terminalDecisionObjective(items []lipapi.Item) string {
	var fallback string
	for _, item := range items {
		text := terminalDecisionItemText(item)
		if text == "" {
			continue
		}
		switch item.Role {
		case lipapi.RoleUser:
			fallback = text
		case lipapi.RoleDeveloper, lipapi.RoleSystem:
			if fallback == "" {
				fallback = text
			}
		}
	}
	return fallback
}

func terminalDecisionRecentText(items []lipapi.Item) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if text := terminalDecisionItemText(item); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func terminalDecisionCandidateText(p *responsePipeline) string {
	if p == nil {
		return ""
	}
	return p.releasedOutputText()
}

func terminalDecisionItemText(item lipapi.Item) string {
	if item.Kind != lipapi.ItemKindMessage {
		return ""
	}
	var parts []string
	for _, part := range item.Content {
		if part.Kind == lipapi.ContentPartText && strings.TrimSpace(part.Text) != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func terminalDecisionCompletionItems(items []lipapi.Item, p *responsePipeline) []lipapi.Item {
	if p == nil {
		return items
	}
	out := append([]lipapi.Item(nil), items...)
	for _, event := range p.seenEventsCopy() {
		if event.Kind == lipapi.EventItem && event.Item != nil {
			out = append(out, *event.Item)
		}
	}
	return out
}

func terminalDecisionActions(items []lipapi.Item, p *responsePipeline) []terminaldecision.ActionFact {
	actions := make([]terminaldecision.ActionFact, 0, terminaldecision.MaxEvidenceActions)
	positions := make(map[string]int, terminaldecision.MaxEvidenceActions)
	add := func(action terminaldecision.ActionFact, key string) {
		if action.ItemID == "" && action.CallID == "" {
			return
		}
		if action.Kind != lipapi.ItemKindMessage && (action.CallID == "" || strings.TrimSpace(action.Name) == "") {
			return
		}
		if index, ok := positions[key]; ok {
			if terminalDecisionActionRank(action.Status) > terminalDecisionActionRank(actions[index].Status) {
				actions[index].Status = action.Status
			}
			if actions[index].Name == "" {
				actions[index].Name = action.Name
			}
			return
		}
		if len(actions) >= terminaldecision.MaxEvidenceActions {
			return
		}
		positions[key] = len(actions)
		actions = append(actions, action)
	}
	for _, item := range items {
		if action, key, ok := terminalDecisionActionFromItem(item); ok {
			add(action, key)
		}
	}
	if p != nil {
		for _, event := range p.seenEventsCopy() {
			action, key, ok := terminalDecisionActionFromEvent(event)
			if ok {
				add(action, key)
			}
		}
	}
	return actions
}

func terminalDecisionActionFromItem(item lipapi.Item) (terminaldecision.ActionFact, string, bool) {
	status := item.Status
	if status == "" {
		status = lipapi.ItemStatusCompleted
	}
	action := terminaldecision.ActionFact{
		ItemID: boundedTerminalDecisionIdentifier(item.ID),
		Kind:   item.Kind,
		Status: status,
	}
	switch item.Kind {
	case lipapi.ItemKindMessage:
		return action, "message:" + item.ID, action.ItemID != ""
	case lipapi.ItemKindToolCall:
		if item.ToolCall == nil {
			return terminaldecision.ActionFact{}, "", false
		}
		action.CallID = boundedTerminalDecisionIdentifier(item.ToolCall.CallID)
		action.Name = boundedTerminalDecisionIdentifier(item.ToolCall.Name)
		return action, "tool_call:" + item.ToolCall.CallID, action.CallID != "" && action.Name != ""
	case lipapi.ItemKindToolResult:
		if item.ToolResult == nil {
			return terminaldecision.ActionFact{}, "", false
		}
		action.CallID = boundedTerminalDecisionIdentifier(item.ToolResult.CallID)
		action.Name = boundedTerminalDecisionIdentifier(item.ToolResult.Name)
		return action, "tool_result:" + item.ToolResult.CallID, action.CallID != "" && action.Name != ""
	default:
		return terminaldecision.ActionFact{}, "", false
	}
}

func terminalDecisionActionFromEvent(event lipapi.Event) (terminaldecision.ActionFact, string, bool) {
	switch event.Kind {
	case lipapi.EventToolCallStarted:
		return terminaldecision.ActionFact{
			CallID: boundedTerminalDecisionIdentifier(event.ToolCallID),
			Kind:   lipapi.ItemKindToolCall,
			Status: lipapi.ItemStatusInProgress,
			Name:   boundedTerminalDecisionIdentifier(event.ToolName),
		}, "tool_call:" + event.ToolCallID, event.ToolCallID != "" && event.ToolName != ""
	case lipapi.EventToolCallFinished:
		return terminaldecision.ActionFact{
			CallID: boundedTerminalDecisionIdentifier(event.ToolCallID),
			Kind:   lipapi.ItemKindToolCall,
			Status: lipapi.ItemStatusCompleted,
		}, "tool_call:" + event.ToolCallID, event.ToolCallID != ""
	case lipapi.EventItem:
		if event.Item == nil {
			return terminaldecision.ActionFact{}, "", false
		}
		return terminalDecisionActionFromItem(*event.Item)
	default:
		return terminaldecision.ActionFact{}, "", false
	}
}

func terminalDecisionActionRank(status lipapi.ItemStatus) int {
	switch status {
	case lipapi.ItemStatusCompleted:
		return 3
	case lipapi.ItemStatusIncomplete:
		return 2
	case lipapi.ItemStatusInProgress:
		return 1
	default:
		return 0
	}
}

func boundedTerminalDecisionIdentifier(value string) string {
	return boundedTerminalDecisionString(value, terminaldecision.MaxIdentifierBytes)
}

func boundedTerminalDecisionText(value string) string {
	return boundedTerminalDecisionString(value, terminaldecision.MaxEvidenceTextBytes)
}

func boundedTerminalDecisionString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}
