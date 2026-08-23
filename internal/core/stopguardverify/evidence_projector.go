package stopguardverify

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Bounded projection limits.
const (
	MaxEvidenceBytes              = 8192
	MaxObjectiveTextBytes         = 2048
	MaxCandidateTextBytes         = 2048
	MaxTrajectorySummaryBytes     = 2048
	MaxCandidateSummaryLines      = 20
	MaxTrajectorySummaryLines     = 20
	MaxToolSummaryLinesPerSection = 10
)

// ProjectEvidence produces a bounded textual evidence block from canonical
// slices without creating a second transcript store. The caller-supplied
// slices are not retained.
func ProjectEvidence(ev stopguard.Evidence) string {
	var b strings.Builder
	b.Grow(2048)

	// Header: cause, flags, attempt, lineage.
	fmt.Fprintf(&b, "Cause: %s\n", ev.Cause)
	fmt.Fprintf(&b, "OutputCommitted: %t\n", ev.OutputCommitted)
	fmt.Fprintf(&b, "ExplicitCompletion: %t\n", ev.ExplicitCompletion)
	fmt.Fprintf(&b, "RecoveryAttempt: %d\n", ev.RecoveryAttempt)
	if ev.ContinuationLineage.ContinuationID != "" {
		fmt.Fprintf(&b, "ContinuationID: %s\n", truncateString(ev.ContinuationLineage.ContinuationID, 256))
	}
	if ev.ParentTraceID != "" {
		fmt.Fprintf(&b, "ParentTraceID: %s\n", truncateString(ev.ParentTraceID, 256))
	}
	if ev.ParentALegID != "" {
		fmt.Fprintf(&b, "ParentALegID: %s\n", truncateString(ev.ParentALegID, 256))
	}
	if ev.ParentBLegID != "" {
		fmt.Fprintf(&b, "ParentBLegID: %s\n", truncateString(ev.ParentBLegID, 256))
	}
	if ev.ParentBranchBinding != "" {
		fmt.Fprintf(&b, "ParentBranchBinding: %s\n", truncateString(ev.ParentBranchBinding, 256))
	}

	// Tool state summary (name + status only, NO raw args/results).
	fmt.Fprintf(&b, "ToolState: completed=%d pending=%s incomplete_args=%t opaque=%t\n",
		ev.ToolState.CompletedToolResults,
		truncateString(ev.ToolState.PendingToolCallID, 256),
		ev.ToolState.HasIncompleteArguments,
		ev.ToolState.HasUnsupportedOpaqueState,
	)

	// User objective: recent user objective text, bounded.
	objText := extractMessageText(ev.UserObjective)
	objText = truncateString(objText, MaxObjectiveTextBytes)
	b.WriteString("UserObjective:\n")
	if objText == "" {
		b.WriteString("(none)\n")
	} else {
		b.WriteString(objText)
		if !strings.HasSuffix(objText, "\n") {
			b.WriteString("\n")
		}
	}

	// Candidate assistant text.
	candText := extractItemsText(ev.CandidateAssistant)
	candText = truncateString(candText, MaxCandidateTextBytes)
	b.WriteString("CandidateAssistant:\n")
	if candText == "" {
		b.WriteString("(none)\n")
	} else {
		b.WriteString(candText)
		if !strings.HasSuffix(candText, "\n") {
			b.WriteString("\n")
		}
	}

	// Recent trajectory summary (bounded lines, name+status only).
	trajSummary := projectTrajectorySummary(ev.RecentTrajectory)
	trajSummary = truncateString(trajSummary, MaxTrajectorySummaryBytes)
	b.WriteString("RecentTrajectory:\n")
	if trajSummary == "" {
		b.WriteString("(none)\n")
	} else {
		b.WriteString(trajSummary)
		if !strings.HasSuffix(trajSummary, "\n") {
			b.WriteString("\n")
		}
	}

	// Candidate trajectory tool summary (for completeness, also bounded).
	candSummary := projectTrajectorySummary(ev.CandidateAssistant)
	candSummary = truncateString(candSummary, MaxTrajectorySummaryBytes)
	b.WriteString("CandidateToolSummary:\n")
	if candSummary == "" {
		b.WriteString("(none)\n")
	} else {
		b.WriteString(candSummary)
		if !strings.HasSuffix(candSummary, "\n") {
			b.WriteString("\n")
		}
	}

	out := b.String()
	if len(out) > MaxEvidenceBytes {
		out = truncateString(out, MaxEvidenceBytes)
	}
	return out
}

func extractMessageText(msgs []lipapi.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	var parts []string
	for _, m := range msgs {
		for _, p := range m.Parts {
			switch p.Kind {
			case lipapi.PartText:
				if s := strings.TrimSpace(p.Text); s != "" {
					parts = append(parts, s)
				}
			case lipapi.PartReasoning:
				// Reasoning is not user objective; skip.
			default:
				// Include only text for bounded evidence; other kinds are referenced
				// via summaries.
				if s := strings.TrimSpace(p.Text); s != "" {
					parts = append(parts, s)
				}
			}
		}
	}
	text := strings.Join(parts, "\n")
	// Digest large payloads: if > limit, keep prefix and append digest marker.
	if len(text) > MaxObjectiveTextBytes {
		prefix := truncateString(text, MaxObjectiveTextBytes-64)
		h := sha256.Sum256([]byte(text))
		text = prefix + fmt.Sprintf("\n[truncated sha256:%s]", hex.EncodeToString(h[:8]))
	}
	return text
}

func extractItemsText(items []lipapi.Item) string {
	if len(items) == 0 {
		return ""
	}
	var parts []string
	for _, it := range items {
		switch it.Kind {
		case lipapi.ItemKindMessage:
			for _, cp := range it.Content {
				switch cp.Kind {
				case lipapi.ContentPartText:
					if s := strings.TrimSpace(cp.Text); s != "" {
						parts = append(parts, s)
					}
				case lipapi.ContentPartSummary:
					if s := strings.TrimSpace(cp.Summary); s != "" {
						parts = append(parts, s)
					}
				case lipapi.ContentPartRefusal:
					if s := strings.TrimSpace(cp.Refusal); s != "" {
						parts = append(parts, s)
					}
				default:
					// Other content kinds are summarized via tool summary, not raw payload.
				}
			}
		default:
			// Non-message items are covered in tool summary; skip raw payload.
		}
		if len(parts) >= MaxCandidateSummaryLines {
			break
		}
	}
	text := strings.Join(parts, "\n")
	if len(text) > MaxCandidateTextBytes {
		prefix := truncateString(text, MaxCandidateTextBytes-64)
		h := sha256.Sum256([]byte(text))
		text = prefix + fmt.Sprintf("\n[truncated sha256:%s]", hex.EncodeToString(h[:8]))
	}
	return text
}

func projectTrajectorySummary(items []lipapi.Item) string {
	if len(items) == 0 {
		return ""
	}
	var lines []string
	count := 0
	for _, it := range items {
		switch it.Kind {
		case lipapi.ItemKindToolCall:
			if it.ToolCall == nil {
				continue
			}
			name := truncateString(it.ToolCall.Name, 128)
			callID := truncateString(it.ToolCall.CallID, 128)
			status := string(it.Status)
			if status == "" {
				status = "in_progress"
			}
			// NO raw args; bounded digest only.
			digest := ""
			if len(it.ToolCall.Arguments) > 0 {
				h := sha256.Sum256(it.ToolCall.Arguments)
				digest = fmt.Sprintf(" args_sha256:%s", hex.EncodeToString(h[:8]))
			}
			lines = append(lines, fmt.Sprintf("tool_call name=%s call_id=%s status=%s%s", name, callID, status, digest))
			count++
		case lipapi.ItemKindToolResult:
			if it.ToolResult == nil {
				continue
			}
			name := truncateString(it.ToolResult.Name, 128)
			callID := truncateString(it.ToolResult.CallID, 128)
			status := string(it.Status)
			if status == "" {
				status = "completed"
			}
			// NO raw output; bounded digest only.
			digest := ""
			if it.ToolResult.Output != "" {
				h := sha256.Sum256([]byte(it.ToolResult.Output))
				digest = fmt.Sprintf(" output_sha256:%s", hex.EncodeToString(h[:8]))
			} else if len(it.ToolResult.Parts) > 0 {
				var joined string
				for _, cp := range it.ToolResult.Parts {
					joined += cp.Text
				}
				if joined != "" {
					h := sha256.Sum256([]byte(joined))
					digest = fmt.Sprintf(" output_sha256:%s", hex.EncodeToString(h[:8]))
				}
			}
			lines = append(lines, fmt.Sprintf("tool_result name=%s call_id=%s status=%s%s", name, callID, status, digest))
			count++
		case lipapi.ItemKindItemReference:
			if it.Reference != nil {
				lines = append(lines, fmt.Sprintf("item_reference id=%s", truncateString(it.Reference.ID, 128)))
				count++
			}
		case lipapi.ItemKindMessage:
			// Message items are textual; tool summary focuses on tool items.
			// Optionally note message presence.
			if count < MaxToolSummaryLinesPerSection {
				role := string(it.Role)
				if role == "" {
					role = "assistant"
				}
				contentKinds := ""
				for _, cp := range it.Content {
					contentKinds += string(cp.Kind) + ","
				}
				contentKinds = strings.TrimSuffix(contentKinds, ",")
				lines = append(lines, fmt.Sprintf("message role=%s kinds=%s status=%s", role, contentKinds, it.Status))
				count++
			}
		default:
			lines = append(lines, fmt.Sprintf("item kind=%s status=%s", it.Kind, it.Status))
			count++
		}
		if count >= MaxToolSummaryLinesPerSection {
			lines = append(lines, "[truncated: more items omitted]")
			break
		}
	}
	return strings.Join(lines, "\n")
}

func truncateString(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	// Avoid cutting in middle of UTF-8 rune.
	for len(cut) > 0 && cut[len(cut)-1]&0xC0 == 0x80 {
		cut = cut[:len(cut)-1]
	}
	return cut
}
