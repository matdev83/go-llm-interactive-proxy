package largebody

import (
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ErrSemanticFactBudgetExceeded indicates that normalized item/part metadata
// or fact attributes exceeded the configured semantic-fact budget, requiring
// pre-commit fallback to canonical processing (Requirement 14.4).
var ErrSemanticFactBudgetExceeded = errors.New("largebody: semantic-fact budget exceeded (optimization decline)")

// IsSemanticFactBudgetExceeded reports whether err is or wraps ErrSemanticFactBudgetExceeded.
func IsSemanticFactBudgetExceeded(err error) bool {
	return errors.Is(err, ErrSemanticFactBudgetExceeded)
}

// MetadataBytes returns the estimated in-memory metadata size of the turn shape
// (excluding any payload prompt text, which is never retained).
func (s ClientTurnShape) MetadataBytes() int64 {
	var bytes int64
	for _, it := range s.Items {
		bytes += int64(len(it.Kind) + len(it.Role) + 8)
		for _, p := range it.Parts {
			bytes += int64(len(p.Kind) + 8)
		}
	}
	return bytes
}

func buildTurnShape(count int, maxFactBytes int64, buildItem func(ord int) (lipapi.ItemKind, lipapi.Role, []ClientTurnPartShape, error)) (ClientTurnShape, error) {
	if err := checkBudget(maxFactBytes); err != nil {
		return ClientTurnShape{}, err
	}
	if int64(count) > maxFactBytes {
		return ClientTurnShape{}, fmt.Errorf("%w: item count %d exceeds budget %d", ErrSemanticFactBudgetExceeded, count, maxFactBytes)
	}
	items := make([]ClientTurnItemShape, 0, count)
	var totalBytes int64
	for ord := 0; ord < count; ord++ {
		kind, role, parts, err := buildItem(ord)
		if err != nil {
			return ClientTurnShape{}, err
		}
		for _, p := range parts {
			totalBytes += p.ContentBytes
		}
		items = append(items, ClientTurnItemShape{Kind: kind, Role: role, Ordinal: int64(ord), Parts: parts})
	}
	shape := ClientTurnShape{Items: items, TotalContentBytes: totalBytes}
	return shape, shape.Validate(maxFactBytes)
}

// ClientTurnShapeFromMessages derives a bounded ClientTurnShape from a message slice
// without synthesizing a lipapi.Call. It maps message parts with exact equivalence
// to canonical partsToContentParts without allocating payload-sized strings.
func ClientTurnShapeFromMessages(msgs []lipapi.Message, maxFactBytes int64) (ClientTurnShape, error) {
	return buildTurnShape(len(msgs), maxFactBytes, func(ord int) (lipapi.ItemKind, lipapi.Role, []ClientTurnPartShape, error) {
		m := msgs[ord]
		parts := make([]ClientTurnPartShape, 0, len(m.Parts))
		for _, p := range m.Parts {
			if ps, keep := partShape(p); keep {
				parts = append(parts, ps)
			}
		}
		if int64(len(parts)) > maxFactBytes {
			return "", "", nil, fmt.Errorf("%w: item %d part count %d exceeds budget %d", ErrSemanticFactBudgetExceeded, ord, len(parts), maxFactBytes)
		}
		return lipapi.ItemKindMessage, m.Role, parts, nil
	})
}

// ClientTurnShapeFromItems derives a bounded ClientTurnShape from normalized items
// without retaining or materializing prompt text.
func ClientTurnShapeFromItems(items []lipapi.Item, maxFactBytes int64) (ClientTurnShape, error) {
	return buildTurnShape(len(items), maxFactBytes, func(ord int) (lipapi.ItemKind, lipapi.Role, []ClientTurnPartShape, error) {
		it := items[ord]
		var parts []ClientTurnPartShape
		if it.Kind == lipapi.ItemKindMessage {
			if int64(len(it.Content)) > maxFactBytes {
				return "", "", nil, fmt.Errorf("%w: item %d part count %d exceeds budget %d", ErrSemanticFactBudgetExceeded, ord, len(it.Content), maxFactBytes)
			}
			parts = make([]ClientTurnPartShape, 0, len(it.Content))
			for _, cp := range it.Content {
				parts = append(parts, ClientTurnPartShape{
					Kind:         cp.Kind,
					ContentBytes: contentPartByteSize(cp),
				})
			}
		}
		return it.Kind, it.Role, parts, nil
	})
}

// ClientTurnShapeFromCall derives a bounded ClientTurnShape equivalent to
// lipapi.NormalizedItems for the given call without retaining or materializing
// prompt text (Requirements 14.3, 14.5).
// If call is nil or has no items, an empty ClientTurnShape is returned.
// If the derived shape exceeds maxFactBytes, it returns an error wrapping
// ErrSemanticFactBudgetExceeded (Requirement 14.4).
func ClientTurnShapeFromCall(call *lipapi.Call, maxFactBytes int64) (ClientTurnShape, error) {
	if call == nil {
		return ClientTurnShapeFromItems(nil, maxFactBytes)
	}
	return ClientTurnShapeFromItems(lipapi.NormalizedItems(*call), maxFactBytes)
}

func contentPartByteSize(cp lipapi.ContentPart) int64 {
	switch cp.Kind {
	case lipapi.ContentPartText, lipapi.ContentPartJSON, lipapi.ContentPartToolResult:
		return int64(len(cp.Text))
	case lipapi.ContentPartImageRef:
		return int64(len(cp.ImageRef) + len(cp.ImageMIME))
	case lipapi.ContentPartFileRef:
		return int64(len(cp.FileRef) + len(cp.FileData) + len(cp.FileMIME) + len(cp.FileName))
	case lipapi.ContentPartVideoRef:
		return int64(len(cp.VideoRef) + len(cp.VideoMIME))
	case lipapi.ContentPartReasoning:
		if cp.Reasoning != nil {
			return int64(len(cp.Reasoning.Text))
		}
		return 0
	case lipapi.ContentPartRefusal:
		return int64(len(cp.Refusal))
	case lipapi.ContentPartSummary:
		return int64(len(cp.Summary))
	case lipapi.ContentPartAssistantRef:
		return int64(len(cp.AssistantRef))
	case lipapi.ContentPartExtension:
		if cp.Extension != nil {
			return int64(len(cp.Extension.Data))
		}
		return 0
	default:
		return int64(len(cp.Text))
	}
}

func partShape(p lipapi.Part) (ClientTurnPartShape, bool) {
	switch p.Kind {
	case lipapi.PartText:
		return ClientTurnPartShape{Kind: lipapi.ContentPartText, ContentBytes: int64(len(p.Text))}, true
	case lipapi.PartImageRef:
		return ClientTurnPartShape{Kind: lipapi.ContentPartImageRef, ContentBytes: int64(len(p.ImageRef) + len(p.ImageMIME))}, true
	case lipapi.PartFileRef:
		return ClientTurnPartShape{Kind: lipapi.ContentPartFileRef, ContentBytes: int64(len(p.FileRef) + len(p.FileMIME) + len(p.FileName))}, true
	case lipapi.PartReasoning:
		var n int64
		if p.Reasoning != nil {
			n = int64(len(p.Reasoning.Text))
		}
		return ClientTurnPartShape{Kind: lipapi.ContentPartReasoning, ContentBytes: n}, true
	case lipapi.PartToolResult:
		return ClientTurnPartShape{Kind: lipapi.ContentPartToolResult, ContentBytes: int64(len(p.Text))}, true
	case lipapi.PartJSON:
		return ClientTurnPartShape{Kind: lipapi.ContentPartJSON, ContentBytes: int64(len(p.Content))}, true
	default:
		if p.Text != "" {
			return ClientTurnPartShape{Kind: lipapi.ContentPartText, ContentBytes: int64(len(p.Text))}, true
		}
		return ClientTurnPartShape{}, false
	}
}
