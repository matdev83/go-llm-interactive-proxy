package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/localstream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/localturn"
)

// localTurnOutcome is the result of the two-phase local-turn stage.
type localTurnOutcome struct {
	claimed      bool
	mergedSnap   conversationview.Snapshot
	stream       lipapi.EventStream
	handlerID    string
	sourceReason localturn.ReasonCode
}

// localTurnHandlers returns the frozen ordered handler list from the snapshot.
func (e *Executor) localTurnHandlers() []localturn.Handler {
	if e == nil || e.RuntimeSnapshot == nil {
		return nil
	}
	return e.RuntimeSnapshot.LocalTurnHandlersExecution()
}

// normalizedMessageCount returns the number of complete normalized messages
// available for claiming in call. For item authority, count ItemKindMessage;
// for legacy, count Instructions + Messages.
func normalizedMessageCount(call lipapi.Call) int {
	if call.HasItemAuthority() {
		c := 0
		for _, it := range call.Items {
			if it.Kind == lipapi.ItemKindMessage {
				c++
			}
		}
		return c
	}
	return len(call.Instructions) + len(call.Messages)
}

// messageIdentityAt returns the MessageIdentity for the normalized message at idx.
func messageIdentityAt(call lipapi.Call, idx int) (conversationview.MessageIdentity, error) {
	if call.HasItemAuthority() {
		n := 0
		for _, it := range call.Items {
			if it.Kind != lipapi.ItemKindMessage {
				continue
			}
			if n == idx {
				return conversationview.ItemIdentityOf(it)
			}
			n++
		}
		return "", fmt.Errorf("localturn: index %d out of range", idx)
	}
	combined := 0
	// Instructions first, then Messages (projectLegacy order)
	if idx < len(call.Instructions) {
		return conversationview.MessageIdentityOf(call.Instructions[idx])
	}
	combined = len(call.Instructions)
	msgIdx := idx - combined
	if msgIdx < 0 || msgIdx >= len(call.Messages) {
		return "", fmt.Errorf("localturn: index %d out of range", idx)
	}
	return conversationview.MessageIdentityOf(call.Messages[msgIdx])
}

// mergeTagsIntoSnapshot returns a new snapshot with Tags from result merged
// without a second store read. It updates StateRevision and NeverBackend.
func mergeTagsIntoSnapshot(base conversationview.Snapshot, result conversationview.TagResult) conversationview.Snapshot {
	out := base
	out.StateRevision = result.StateRevision
	// Deep copy tags (already sorted by store)
	tags := make([]conversationview.Tag, len(result.Tags))
	copy(tags, result.Tags)
	out.NeverBackend = tags
	return out
}

// localTurnSourceRequests builds TagRequests for claimed source indexes.
func localTurnSourceRequests(call lipapi.Call, res localturn.MatchResult) ([]conversationview.TagRequest, error) {
	if !res.Claimed {
		return nil, nil
	}
	if len(res.Indexes) == 0 {
		return nil, fmt.Errorf("localturn: claimed with empty indexes")
	}
	out := make([]conversationview.TagRequest, 0, len(res.Indexes))
	for _, idx := range res.Indexes {
		id, err := messageIdentityAt(call, idx)
		if err != nil {
			return nil, err
		}
		if err := id.Validate(); err != nil {
			return nil, fmt.Errorf("localturn: invalid source identity at %d: %w", idx, err)
		}
		out = append(out, conversationview.TagRequest{
			Identity: id,
			Reason:   conversationview.ReasonCode(res.Reason),
		})
	}
	return out, nil
}

// canonicalReplyMessage constructs the canonical assistant text message for tagging and streaming.
// Delegates to the producer-neutral localstream helper so reply-tag identity
// equals encoded/decoded assistant content across all official frontends.
func canonicalReplyMessage(text string) lipapi.Message {
	return localstream.CanonicalAssistantMessage(text)
}

// buildLocalEventStream constructs a finite EventStream from reply text with
// exactly the same content that was tagged. No provider usage, no B-leg ID,
// no background goroutine; obeys context cancellation/Close.
// Delegates to the generic localstream factory used by frontend contract tests.
func buildLocalEventStream(replyText string) lipapi.EventStream {
	return localstream.NewTextStream(replyText)
}

// runLocalTurnStage executes the two-phase local-turn protocol against the
// preserved ingress view. It is pure Match against ingress, validates source
// indexes, tags source before Handle, merges tags into request-local snapshot,
// invokes Handle with panic recovery, tags reply before stream release, and
// returns a finite stream. No second store read, no billing/route/B-leg.
func (e *Executor) runLocalTurnStage(ctx context.Context, ingress lipapi.Call, snap conversationview.Snapshot, handlers []localturn.Handler, tagger conversationview.Tagger, aLegID string, traceID string) (localTurnOutcome, error) {
	if len(handlers) == 0 || tagger == nil {
		return localTurnOutcome{}, nil
	}
	if strings.TrimSpace(aLegID) == "" {
		return localTurnOutcome{}, fmt.Errorf("executor: localturn: missing aLegID")
	}
	msgCount := normalizedMessageCount(ingress)
	meta := localturn.Meta{TraceID: traceID, MessageCount: msgCount}
	// Deterministic order already via MaterializeSorted at snapshot construction,
	// but ensure stable iteration: handlers are already sorted.
	for _, h := range handlers {
		if localturn.IsNilHandler(h) {
			continue
		}
		// Pure Match phase against preserved ingress
		mr, merr := h.Match(ctx, ingress, meta)
		if merr != nil {
			// Pre-claim errors obey fail-open/fail-closed
			if h.FailureMode() == localturn.FailOpen {
				continue
			}
			return localTurnOutcome{}, fmt.Errorf("executor: localturn match %s: %w", h.ID(), merr)
		}
		if !mr.Claimed {
			continue
		}
		// Validate claimed result
		if err := mr.Validate(meta); err != nil {
			return localTurnOutcome{}, fmt.Errorf("executor: localturn %s invalid match: %w", h.ID(), err)
		}
		// Build source tag requests and validate identities before Handle
		srcReqs, err := localTurnSourceRequests(ingress, mr)
		if err != nil {
			return localTurnOutcome{}, fmt.Errorf("executor: localturn %s source identity: %w", h.ID(), err)
		}
		// Persist source tags BEFORE Handle; merge without second store read
		tagRes, err := tagger.TagNeverBackend(ctx, aLegID, srcReqs)
		if err != nil {
			return localTurnOutcome{}, fmt.Errorf("executor: localturn %s source tag: %w", h.ID(), err)
		}
		merged := mergeTagsIntoSnapshot(snap, tagRes)
		// Invoke Handle with panic recovery; after claim any error/panic/invalid reply fails request with no fallback
		var reply localturn.Reply
		var herr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					herr = fmt.Errorf("executor: localturn %s panic: %v", h.ID(), r)
				}
			}()
			reply, herr = h.Handle(ctx, localturn.HandleInput{Call: ingress, Meta: meta, Match: mr})
		}()
		if herr != nil {
			return localTurnOutcome{}, fmt.Errorf("executor: localturn %s handle: %w", h.ID(), herr)
		}
		if err := reply.Validate(); err != nil {
			return localTurnOutcome{}, fmt.Errorf("executor: localturn %s invalid reply: %w", h.ID(), err)
		}
		// Construct canonical assistant message and tag reply BEFORE releasing event
		cmsg := canonicalReplyMessage(reply.Text)
		rid, err := conversationview.MessageIdentityOf(cmsg)
		if err != nil {
			return localTurnOutcome{}, fmt.Errorf("executor: localturn %s reply identity: %w", h.ID(), err)
		}
		replyReq := []conversationview.TagRequest{{Identity: rid, Reason: conversationview.ReasonCode(mr.Reason)}}
		replyTagRes, err := tagger.TagNeverBackend(ctx, aLegID, replyReq)
		if err != nil {
			return localTurnOutcome{}, fmt.Errorf("executor: localturn %s reply tag: %w", h.ID(), err)
		}
		merged2 := mergeTagsIntoSnapshot(merged, replyTagRes)
		stream := buildLocalEventStream(reply.Text)
		return localTurnOutcome{
			claimed:      true,
			mergedSnap:   merged2,
			stream:       stream,
			handlerID:    h.ID(),
			sourceReason: mr.Reason,
		}, nil
	}
	return localTurnOutcome{}, nil
}
