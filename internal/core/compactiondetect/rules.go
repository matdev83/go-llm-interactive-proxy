package compactiondetect

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
)

// ruleMode declares the transaction behavior of a rule (requirement 6.1).
type ruleMode int

const (
	// modeSingle: the first matching opened request emits a start; a strict
	// later completion closes it; otherwise the transaction closes silently.
	modeSingle ruleMode = iota
	// modeSeries: later matching utility subcalls reuse one active transaction
	// and suppress repeated starts; the first strict/heuristic completion closes.
	modeSeries
	// modeCompletionOnly: no start is ever emitted; a post marker or heuristic
	// creates one completed event transaction.
	modeCompletionOnly
)

// requestInfo is the precomputed canonical view of one opened request used by
// start predicates. Only normalized canonical role/kind/content data is used;
// no provider DTO or wire payload is ever inspected. Text is folded once for
// deterministic, case-insensitive marker matching.
type requestInfo struct {
	call  lipapi.Call
	lower string
}

func (r requestInfo) hasText(marker string) bool {
	// Marker constants are stored lowercase; r.lower is the once-folded request
	// text, so matching is an allocation-free strings.Contains.
	return r.lower != "" && strings.Contains(r.lower, marker)
}

func (r requestInfo) toolCount() int {
	n := len(r.call.Tools)
	_ = lipapi.WalkCallItems(r.call, func(item lipapi.Item) error {
		if item.ToolCall != nil {
			n++
		}
		return nil
	})
	return n
}

func (r requestInfo) noTools() bool {
	return r.toolCount() == 0
}

// rule is one versioned detection rule from the surveyed-agent matrix
// (research.md). Rules use explicit match functions over canonical roles,
// items, and text; there is no regex DSL or provider branching.
type rule struct {
	id       string
	mode     ruleMode
	evidence compaction.Evidence
	// start matches the request side of a compaction utility call. Nil for
	// completion-only rules (a completion-only rule never emits a start).
	start func(requestInfo) bool
	// complete matches a released installed-summary/post marker in the
	// lowercased released-text window (folded once per chunk by the detector).
	// Nil when the family exposes no response-side marker (the rule then
	// completes only via strict canonical output or the history heuristic).
	complete func(string) bool
}

// Rule marker constants. They are implementation-owned versioned signatures:
// updating one signature requires one focused rule/table/test change, never a
// provider-adapter change (requirement 4.8). Markers are stored in lowercase:
// the request text is folded once per request and the released-text window is
// folded once per chunk, so matching is a plain allocation-free
// strings.Contains and never re-folds accumulated text.
const (
	markerCodexCheckpoint     = "context checkpoint compaction"
	markerConversationTag     = "<conversation>"
	markerPiSummaryCarrier    = "<summary>"
	markerClineSummaryPost    = "context summary:"
	markerSystemNotice        = "<system_notice>"
	markerClineCompactedPost  = "earlier context was compacted"
	markerOpenCodeHistoryHead = "the following is the conversation history:"
	markerHermesRefOnly       = "[context compaction \u2014 reference only]"
	markerHermesLegacySummary = "[context summary]:"
	markerKiloObjective       = "objective"
	markerKiloDetails         = "important details"
	markerKiloWorkState       = "work state"
	markerKiloNextMove        = "next move"
	markerKiloFiles           = "relevant files"
	markerAiderUserTag        = "# user"
	markerAiderAssistantTag   = "# assistant"
)

var (
	// protocolRule is matched before every signature rule so protocol-strict
	// evidence takes precedence over text signatures when both are present
	// (requirement 3.1; task 1.2). Its completion is the released compaction
	// item or the successful terminal of an explicit compact operation.
	protocolRule = rule{
		id:       "protocol.context_compaction.v1",
		mode:     modeSingle,
		evidence: compaction.EvidenceProtocolStrict,
		start: func(r requestInfo) bool {
			return r.call.Invocation.Operation == lipapi.OperationContextCompaction
		},
	}

	// ruleTable is ordered: the first matching rule wins. The protocol rule
	// must stay first so canonical semantics dominate text signatures.
	ruleTable = []rule{
		protocolRule,
		{
			id:       "codex.local_checkpoint.v1",
			mode:     modeSingle,
			evidence: compaction.EvidenceSignatureStrict,
			start: func(r requestInfo) bool {
				return r.hasText(markerCodexCheckpoint)
			},
			complete: func(text string) bool {
				return strings.Contains(text, markerCodexCheckpoint)
			},
		},
		{
			// One shared rule identity for Pi and OpenClaw: indistinguishable
			// harnesses are never assigned an invented identity (R4.4).
			id:       "pi_openclaw.compaction_summary.v1",
			mode:     modeSeries,
			evidence: compaction.EvidenceSignatureStrict,
			start: func(r requestInfo) bool {
				return r.hasText(markerConversationTag) &&
					r.hasText("summar") &&
					r.hasText("checkpoint")
			},
			complete: func(text string) bool {
				return strings.Contains(text, markerPiSummaryCarrier)
			},
		},
		{
			id:       "cline.agentic_compaction.v1",
			mode:     modeSingle,
			evidence: compaction.EvidenceSignatureStrict,
			start: func(r requestInfo) bool {
				return r.hasText("continuation-note") &&
					r.hasText("summar")
			},
			complete: func(text string) bool {
				return strings.Contains(text, markerClineSummaryPost)
			},
		},
		{
			id:       "cline.basic_compaction_post.v1",
			mode:     modeCompletionOnly,
			evidence: compaction.EvidenceSignatureStrict,
			complete: func(text string) bool {
				return strings.Contains(text, markerSystemNotice) &&
					strings.Contains(text, markerClineCompactedPost)
			},
		},
		{
			id:       "opencode.anchored_summary.v1",
			mode:     modeSingle,
			evidence: compaction.EvidenceSignatureStrict,
			start: func(r requestInfo) bool {
				return r.hasText(markerConversationTag) &&
					r.hasText("summar") &&
					r.hasText(markerKiloObjective) &&
					r.hasText(markerKiloWorkState)
			},
		},
		{
			id:       "opencode.custom_compaction_history.v1",
			mode:     modeSingle,
			evidence: compaction.EvidenceSignatureStrict,
			start: func(r requestInfo) bool {
				return r.hasText(markerOpenCodeHistoryHead)
			},
		},
		{
			id:       "hermes.local_compaction_post.v1",
			mode:     modeCompletionOnly,
			evidence: compaction.EvidenceSignatureStrict,
			complete: func(text string) bool {
				return strings.Contains(text, markerHermesRefOnly)
			},
		},
		{
			id:       "hermes.legacy_compaction_post.v1",
			mode:     modeCompletionOnly,
			evidence: compaction.EvidenceSignatureStrict,
			complete: func(text string) bool {
				return strings.Contains(text, markerHermesLegacySummary)
			},
		},
		{
			id:       "kilocode.anchored_summary.v1",
			mode:     modeSingle,
			evidence: compaction.EvidenceSignatureStrict,
			start: func(r requestInfo) bool {
				return r.hasText(markerKiloObjective) &&
					r.hasText(markerKiloDetails) &&
					r.hasText(markerKiloWorkState) &&
					r.hasText(markerKiloNextMove) &&
					r.hasText(markerKiloFiles)
			},
		},
		{
			id:       "claude_code_2026_03.compaction.v1",
			mode:     modeSingle,
			evidence: compaction.EvidenceSignatureStrict,
			start: func(r requestInfo) bool {
				return r.noTools() &&
					r.hasText("text only") &&
					r.hasText("compaction") &&
					r.hasText("conversation")
			},
			complete: func(text string) bool {
				return strings.Contains(text, "continuation") &&
					strings.Contains(text, "previous conversation")
			},
		},
		{
			id:       "gemini_cli.state_snapshot.v1",
			mode:     modeSeries,
			evidence: compaction.EvidenceSignatureStrict,
			start: func(r requestInfo) bool {
				if !r.hasText("state snapshot") {
					return false
				}
				return r.hasText("generate") || r.hasText("verify")
			},
		},
		{
			id:       "roo_code.condense.v1",
			mode:     modeSingle,
			evidence: compaction.EvidenceSignatureStrict,
			start: func(r requestInfo) bool {
				return r.hasText("condense") &&
					r.hasText("summar") &&
					r.hasText("conversation")
			},
		},
		{
			id:       "aider.chat_summary.v1",
			mode:     modeSeries,
			evidence: compaction.EvidenceSignatureStrict,
			start: func(r requestInfo) bool {
				return r.hasText(markerAiderUserTag) &&
					r.hasText(markerAiderAssistantTag) &&
					r.hasText("summar")
			},
			complete: func(text string) bool {
				return strings.Contains(text, "previous conversation")
			},
		},
		{
			id:       "crush.session_summary.v1",
			mode:     modeSingle,
			evidence: compaction.EvidenceSignatureStrict,
			start: func(r requestInfo) bool {
				return r.hasText("session summary") &&
					r.hasText("preserve") &&
					r.hasText("context")
			},
		},
	}
)

// matchStartRule returns the first rule whose request-side start predicate
// matches. Completion-only rules never participate in start matching.
func matchStartRule(info requestInfo) (rule, bool) {
	for _, r := range ruleTable {
		if r.mode == modeCompletionOnly || r.start == nil {
			continue
		}
		if r.start(info) {
			return r, true
		}
	}
	return rule{}, false
}

// matchCompleteRule returns the first rule whose released-text post-marker
// predicate matches. Start-bearing rules may complete only their active
// transaction; completion-only rules may match without an active transaction.
// The protocol rule completes through canonical items and the explicit-compact
// terminal instead of text, so it is not consulted here.
func matchCompleteRule(text, activeRuleID string) (rule, bool) {
	if text == "" {
		return rule{}, false
	}
	for _, r := range ruleTable {
		if r.complete == nil {
			continue
		}
		if r.mode != modeCompletionOnly && r.id != activeRuleID {
			continue
		}
		if r.complete(text) {
			return r, true
		}
	}
	return rule{}, false
}
