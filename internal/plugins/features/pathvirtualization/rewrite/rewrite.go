package rewrite

// This file implements the call walk of design.md 231-252: the one pure step that
// both outbound passes share, over both canonical authorities.
//
// The walk is driven by the canonical surface shape, never by a flag a caller sets,
// and it reads the input while writing a published copy. That is what makes the step
// pure: a rewrite either changes nothing and republishes the call it was given, or it
// publishes a deep copy in which only selected payloads differ, and it never modifies
// a byte the caller owns.
//
// A tool name is the only authority that selects anything, and it is exact: the
// canonical resolver answers a name or nothing, and a near-miss spelling reaches no
// profile. A tool call additionally offers its declared argument schema to the
// optional inference step, so requirement 2.1's "safely inferred" half is reachable
// here, while a result surface never does, because a result has no declared schema
// and requirement 3.2 keeps result selectors explicit.

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Rewriter is the pure canonical outbound rewriter.
//
// It holds one workspace-bound mapping and one compiled profile policy, both
// immutable after construction, so a single rewriter may be shared by every request
// of a generation without synchronization. The zero value and a nil pointer are both
// inactive: they publish the call they were given unchanged.
type Rewriter struct {
	// mapping decides what a value means as a filesystem locator. It is the only
	// authority on whether a real-root prefix is present and ends on a segment
	// boundary, and it is the only place an alias is ever produced.
	mapping pathvirtualization.Mapping
	// resolver answers the exact-name profile precedence for one tool. Its nil
	// receiver is safe and publishes no selector, which is the answer for a tool no
	// layer claims.
	resolver *pathvirtualization.Resolver
}

// New binds one mapping to one compiled profile policy.
//
// It cannot fail. Both arguments are values with no failure mode: a mapping whose
// alias is not shorter than its root simply leaves every surface unchanged, and a
// nil resolver publishes no selector, so a caller that has no policy yet binds a
// rewriter that is off rather than one that guesses.
func New(mapping pathvirtualization.Mapping, resolver *pathvirtualization.Resolver) *Rewriter {
	return &Rewriter{mapping: mapping, resolver: resolver}
}

// RewriteCall returns the call with every selected path value replaced by its alias.
//
// The returned call is the input itself when nothing changed, and a new deep copy
// when something did; the input is never modified either way, and the published copy
// shares no payload byte with it. A caller therefore owns whatever it gets back and
// can keep using the call it passed in.
//
// On an unexpected transformation failure the returned call is the input, the
// statistics are zero, and the error is non-nil. That is the fail-open contract the
// outbound pass needs: a request whose paths could not be rewritten must still reach
// the backend with real paths rather than not reach it at all.
func (r *Rewriter) RewriteCall(call *lipapi.Call) (*lipapi.Call, Stats, error) {
	if call == nil {
		return nil, Stats{}, nil
	}
	if r == nil {
		// An inactive rewriter is a no-op rather than a failure: the call is already
		// canonical and the statistics are empty.
		return call, Stats{}, nil
	}
	walk := &callWalker{rewriter: r, in: call}
	walk.run()
	if walk.err != nil {
		return call, Stats{}, walk.err
	}
	stats := walk.acc.stats()
	if walk.out == nil {
		return call, stats, nil
	}
	return walk.out, stats, nil
}

// callWalker carries one rewrite across a call: the input it reads, the published
// copy it writes on first change, the running accounting, and the first failure.
type callWalker struct {
	rewriter *Rewriter
	in       *lipapi.Call
	out      *lipapi.Call
	acc      account
	err      error
}

// working returns the published call, cloning the input on first use.
//
// Reads always come from the input and writes always go to the clone, so the two
// stay in step: a surface already rewritten is never read again, and every surface
// the clone does not receive is a byte-for-byte copy of the input's.
func (w *callWalker) working() *lipapi.Call {
	if w.out == nil {
		// The canonical clone is a deep copy, so the published call can never be
		// reached through a payload byte the input still owns.
		clone := lipapi.CloneCall(*w.in)
		w.out = &clone
	}
	return w.out
}

// run walks the call's authoritative surface once.
//
// The two authorities are exclusive by construction: canonical validation refuses a
// call that carries both ordered items and raw message parts, so the item authority
// is taken from the call itself and otherwise the legacy message parts are walked.
func (w *callWalker) run() {
	if w.rewriter.mapping.VirtualRoot == "" {
		// Requirement 1.4: an alias that does not shorten the root leaves outbound
		// virtualization inactive. The state belongs to the whole call, so it is
		// recorded once instead of once per surface.
		w.acc.skip(SkipReasonMappingInactive)
		return
	}
	if w.in.HasItemAuthority() {
		w.rewriteItems()
		return
	}
	w.rewriteMessages()
}

// rewriteItems walks ordered item authority.
func (w *callWalker) rewriteItems() {
	for i := range w.in.Items {
		if w.err != nil {
			// One failure ends the walk: a caller that fails open publishes the input
			// call, so a partially rewritten result could never escape.
			return
		}
		item := &w.in.Items[i]
		switch {
		case item.Kind == lipapi.ItemKindToolCall && item.ToolCall != nil:
			w.rewriteToolCall(item.ToolCall.Name, item.ToolCall.Arguments,
				func(raw []byte) { w.working().Items[i].ToolCall.Arguments = raw })
		case item.Kind == lipapi.ItemKindToolResult && item.ToolResult != nil:
			w.rewriteToolResult(i, item.ToolResult)
		}
		// Every other item kind is ordinary conversation content: a message, a
		// reference, reasoning, compaction, or an extension. This feature never
		// inspects or rewrites any of it (requirement 2.7).
	}
}

// rewriteMessages walks legacy message-part authority.
//
// Message role is deliberately not consulted. Anthropic delivers tool results inside
// a user message and Gemini function calls with no call ID, so role is not a
// reliable discriminator across dialects, while the part kind plus the canonical tool
// name is exactly what every frontend that produces these parts sets.
func (w *callWalker) rewriteMessages() {
	for i := range w.in.Messages {
		parts := w.in.Messages[i].Parts
		for j := range parts {
			if w.err != nil {
				return
			}
			part := parts[j]
			switch part.Kind {
			case lipapi.PartJSON:
				if part.ToolName == "" {
					// A JSON part with no canonical tool name is assistant content or
					// a reasoning payload, not a tool call. There is no tool to resolve
					// an exact profile against, so it is not a path-bearing surface at
					// all and is left alone without a reason (requirement 2.7).
					continue
				}
				w.rewriteToolCall(part.ToolName, part.Content,
					func(raw []byte) { w.working().Messages[i].Parts[j].Content = raw })
			case lipapi.PartToolResult:
				w.rewriteLegacyToolResult(i, j, part)
			}
		}
	}
}

// rewriteToolCall resolves one tool's policy and rewrites its argument payload.
func (w *callWalker) rewriteToolCall(toolName string, arguments []byte, assign func([]byte)) {
	// Only an argument surface offers its declared schema to the inference step: it
	// is the one surface the tool contract declares, and it is what makes a safely
	// inferred location reachable at all (requirement 2.1).
	resolved := w.rewriter.resolver.Resolve(toolName, w.declaredSchema(toolName))
	w.rewritePayload(arguments, resolved.ArgPointers, assign)
}

// rewriteToolResult handles the item-authoritative result surfaces.
//
// Its Output field and every text-bearing content part are opaque, so they are left
// exactly as they arrived and reported under the reason that matches the tool's
// declared mode. A JSON content part is the structured surface requirement 2.2 and
// 3.2 address, and it is rewritten only through an explicitly selected location.
func (w *callWalker) rewriteToolResult(index int, result *lipapi.ToolResultItem) {
	if w.err != nil {
		return
	}
	// A result has no declared schema, so nothing is offered to the inference step:
	// requirement 3.2 keeps structured result selection explicit.
	resolved := w.rewriter.resolver.Resolve(result.Name, nil)
	opaque := opaqueReason(resolved)
	if result.Output != "" {
		w.acc.skip(opaque)
	}
	for j := range result.Parts {
		part := result.Parts[j]
		switch part.Kind {
		case lipapi.ContentPartJSON:
			w.rewritePayload([]byte(part.Text), resolved.ResultJSONPointers,
				func(raw []byte) { w.working().Items[index].ToolResult.Parts[j].Text = string(raw) })
		case lipapi.ContentPartToolResult:
			if part.Text != "" {
				w.acc.skip(opaque)
			}
		default:
			// A text, image, file, or extension part carries no structured result
			// surface, and prose is never a path-bearing location.
		}
	}
}

// rewriteLegacyToolResult handles the legacy result part surfaces.
//
// The part's Content is the structured result JSON a frontend decoded from a wire
// result object, so it is the structured surface this feature may rewrite, and its
// Text is an opaque payload a client or provider spelled as prose.
func (w *callWalker) rewriteLegacyToolResult(message, part int, value lipapi.Part) {
	if w.err != nil {
		return
	}
	resolved := w.rewriter.resolver.Resolve(value.ToolName, nil)
	opaque := opaqueReason(resolved)
	if !isAbsentOrNullPayload(value.Content) {
		w.rewritePayload(value.Content, resolved.ResultJSONPointers,
			func(raw []byte) { w.working().Messages[message].Parts[part].Content = raw })
	}
	if value.Text != "" {
		w.acc.skip(opaque)
	}
}

// rewritePayload applies one selector set to one payload and publishes the result.
//
// A surface with no selector at all is reported as such: that is the required answer
// for an unknown tool, and it is what keeps requirement 3.8's refusal of opaque
// rewriting for unknown tools visible without inspecting anything.
func (w *callWalker) rewritePayload(payload []byte, pointers pathvirtualization.SelectorSet, assign func([]byte)) {
	if w.err != nil {
		return
	}
	if len(pointers) == 0 {
		w.acc.skip(SkipReasonNoSelectors)
		return
	}
	rewritten, changed, err := w.rewriter.rewriteDocument(payload, pointers, &w.acc)
	if err != nil {
		w.err = err
		return
	}
	if changed {
		assign(rewritten)
	}
}

// declaredSchema returns the declared argument schema of the exact-named tool, or
// nil when the call declares no such tool.
//
// The comparison is byte-exact, matching the authority the profile layers use, so a
// near-miss spelling never hands one tool's schema to the inference step on behalf of
// another. A tool that declares no schema gives the step nothing to prove, which is
// what keeps an unknown tool from acquiring selectors by accident.
func (w *callWalker) declaredSchema(toolName string) []byte {
	if toolName == "" {
		return nil
	}
	for _, tool := range w.in.Tools {
		if tool.Name == toolName {
			return tool.Parameters
		}
	}
	return nil
}

// opaqueReason returns the bounded reason for leaving one opaque result payload
// unchanged.
//
// The two values separate the required default from a declared one, so an
// accounting stage can tell "nothing marked this result path-oriented" apart from
// "an exact profile marked it path-oriented and this step still does not touch it",
// without either case having to look at the payload.
func opaqueReason(resolved pathvirtualization.Resolved) SkipReason {
	if resolved.OpaqueResultMode != pathvirtualization.OpaqueResultModeNone {
		return SkipReasonOpaqueResultBounded
	}
	return SkipReasonOpaqueResultUnchanged
}
