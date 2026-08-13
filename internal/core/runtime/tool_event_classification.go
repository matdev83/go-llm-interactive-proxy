package runtime

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// toolEventClassification is the remembered derived metadata for one source ToolCallID.
type toolEventClassification struct {
	category  lipapi.ToolCategory
	mayMutate bool
}

// toolEventClassificationState correlates a tool call's derived classification by its
// source ToolCallID within one retryRecvStream. It is owned by the single goroutine
// that drives Recv (retryRecvStream is not multi-Recv-safe), so it needs no mutex,
// goroutine, TTL, or persistence. The map is allocated lazily on first use.
type toolEventClassificationState struct {
	byCallID map[string]toolEventClassification
}

// enrich attaches classification to te before it reaches tool policies/reactors:
// a non-empty name is classified and remembered; a name-less fragment inherits the
// remembered classification for its ID; otherwise it receives the conservative
// unknown/true fallback.
func (st *toolEventClassificationState) enrich(te *lipapi.ToolEvent) {
	if te == nil {
		return
	}
	if strings.TrimSpace(te.ToolName) != "" {
		te.Category, te.MayMutateLocalFS = lipapi.ClassifyToolName(te.ToolName)
		st.remember(te.ToolCallID, te.Category, te.MayMutateLocalFS)
		return
	}
	if cls, ok := st.lookup(te.ToolCallID); ok {
		te.Category = cls.category
		te.MayMutateLocalFS = cls.mayMutate
		return
	}
	te.Category = lipapi.ToolCategoryUnknown
	te.MayMutateLocalFS = true
}

// rememberEffective records the post-reactor effective classification (already
// reconciled by the hook bus) under the source lifecycle ID so later name-less
// fragments inherit it.
func (st *toolEventClassificationState) rememberEffective(sourceID string, te lipapi.ToolEvent) {
	st.remember(sourceID, te.Category, te.MayMutateLocalFS)
}

// observeFinalName refreshes the source lifecycle from a final non-empty tool name
// observed after response-part hooks, so a later name-less fragment inherits the
// classification of the last emitted name.
func (st *toolEventClassificationState) observeFinalName(sourceID string, ev lipapi.Event) {
	te, ok := lipapi.ToolEventFromEvent(ev)
	if !ok || strings.TrimSpace(te.ToolName) == "" {
		return
	}
	st.remember(sourceID, te.Category, te.MayMutateLocalFS)
}

func (st *toolEventClassificationState) remember(id string, cat lipapi.ToolCategory, mut bool) {
	if st == nil || id == "" {
		return
	}
	if st.byCallID == nil {
		st.byCallID = make(map[string]toolEventClassification)
	}
	st.byCallID[id] = toolEventClassification{category: cat, mayMutate: mut}
}

func (st *toolEventClassificationState) lookup(id string) (toolEventClassification, bool) {
	if st == nil || id == "" {
		return toolEventClassification{}, false
	}
	cls, ok := st.byCallID[id]
	return cls, ok
}

// forget removes one source ID's remembered classification (e.g. after a finished
// lifecycle completes processing) so a later reuse of that ID cannot inherit stale state.
func (st *toolEventClassificationState) forget(id string) {
	if st == nil || st.byCallID == nil {
		return
	}
	delete(st.byCallID, id)
}

// clear discards all remembered classification state (e.g. when the owning stream
// resets, is replaced, or terminates).
func (st *toolEventClassificationState) clear() {
	if st != nil {
		st.byCallID = nil
	}
}
