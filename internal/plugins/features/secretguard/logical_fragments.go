package secretguard

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// FragmentKind identifies the canonical representation supplied to a detector.
type FragmentKind uint8

const (
	FragmentText FragmentKind = iota
	FragmentJSON
)

// FragmentKind aliases keep the representation names explicit at call sites.
const (
	FragmentKindText = FragmentText
	FragmentKindJSON = FragmentJSON
)

// LogicalFragment is one bounded canonical content unit. Raw is transient
// request content and must stay inside the feature boundary.
type LogicalFragment struct {
	Location string
	Kind     FragmentKind
	Raw      []byte

	// privateID distinguishes canonical fields that intentionally share a
	// public location (for example tool-result text and raw JSON content).
	// It never crosses the feature-private merger boundary.
	privateID string
	replace   func([]byte)
}

func (f LogicalFragment) setRaw(raw []byte) {
	if f.replace != nil {
		f.replace(raw)
	}
}

type scanBudget struct {
	maxBytes int
	used     int
	limitHit bool
}

func newScanBudget(maxBytes int) *scanBudget {
	if maxBytes <= 0 {
		maxBytes = DefaultScanMaxBytes
	}
	return &scanBudget{maxBytes: maxBytes}
}

// admit reserves one original logical fragment before either detector sees it.
// Derived JSON scalar values never pass through this method.
func (b *scanBudget) admit(n int) bool {
	if b == nil || n <= 0 || b.limitHit {
		return false
	}
	if b.used+n > b.maxBytes {
		b.limitHit = true
		return false
	}
	b.used += n
	return true
}

// walkLogicalFragments emits whole text and raw JSON units in the same stable
// order as the existing exact scanner. A rejected fragment is never emitted.
func walkLogicalFragments(call *lipapi.Call, budget *scanBudget) []LogicalFragment {
	if call == nil {
		return nil
	}
	if budget == nil {
		budget = newScanBudget(0)
	}

	fragments := make([]LogicalFragment, 0)
	appendText := func(location string, value *string) bool {
		if value == nil || len(*value) == 0 || !budget.admit(len(*value)) {
			return budget.limitHit
		}
		fragments = append(fragments, LogicalFragment{
			Location:  location,
			Kind:      FragmentText,
			Raw:       []byte(*value),
			privateID: fmt.Sprintf("fragment[%d]", len(fragments)),
			replace:   func(raw []byte) { *value = string(raw) },
		})
		return false
	}
	appendJSON := func(location string, value *json.RawMessage) bool {
		if value == nil || len(*value) == 0 || !budget.admit(len(*value)) {
			return budget.limitHit
		}
		fragments = append(fragments, LogicalFragment{
			Location:  location,
			Kind:      FragmentJSON,
			Raw:       bytes.Clone(*value),
			privateID: fmt.Sprintf("fragment[%d]", len(fragments)),
			replace:   func(raw []byte) { *value = bytes.Clone(raw) },
		})
		return false
	}
	appendMessage := func(messages []lipapi.Message, prefix string) bool {
		for i := range messages {
			for j := range messages[i].Parts {
				loc := fmt.Sprintf("%s[%d].parts[%d]", prefix, i, j)
				part := &messages[i].Parts[j]
				switch part.Kind {
				case lipapi.PartText:
					if appendText(loc, &part.Text) {
						return true
					}
				case lipapi.PartJSON:
					if appendJSON(loc, &part.Content) {
						return true
					}
				case lipapi.PartToolResult:
					if appendText(loc, &part.Text) {
						return true
					}
					if appendJSON(loc, &part.Content) {
						return true
					}
				}
			}
		}
		return false
	}

	if appendMessage(call.Instructions, "instructions") || appendMessage(call.Messages, "messages") {
		return fragments
	}
	for i := range call.Tools {
		tool := &call.Tools[i]
		if appendText(fmt.Sprintf("tools[%d].name", i), &tool.Name) ||
			appendText(fmt.Sprintf("tools[%d].description", i), &tool.Description) ||
			appendJSON(fmt.Sprintf("tools[%d].schema", i), &tool.Parameters) {
			return fragments
		}
	}
	return fragments
}
