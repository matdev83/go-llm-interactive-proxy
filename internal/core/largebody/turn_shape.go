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

// ClientTurnShapeFromCall derives a bounded ClientTurnShape equivalent to
// lipapi.NormalizedItems for the given call without retaining or materializing
// prompt text (Requirements 14.3, 14.5).
// If call is nil or has no items, an empty ClientTurnShape is returned.
// If the derived shape exceeds maxFactBytes, it returns an error wrapping
// ErrSemanticFactBudgetExceeded (Requirement 14.4).
func ClientTurnShapeFromCall(call *lipapi.Call, maxFactBytes int64) (ClientTurnShape, error) {
	if err := checkBudget(maxFactBytes); err != nil {
		return ClientTurnShape{}, err
	}
	if call == nil {
		return ClientTurnShape{}, nil
	}

	items := lipapi.NormalizedItems(*call)
	if int64(len(items)) > maxFactBytes {
		return ClientTurnShape{}, fmt.Errorf("%w: item count %d exceeds budget %d", ErrSemanticFactBudgetExceeded, len(items), maxFactBytes)
	}

	shapeItems := make([]ClientTurnItemShape, 0, len(items))
	var totalContentBytes int64

	for ord, it := range items {
		var parts []ClientTurnPartShape
		if it.Kind == lipapi.ItemKindMessage {
			if int64(len(it.Content)) > maxFactBytes {
				return ClientTurnShape{}, fmt.Errorf("%w: item %d part count %d exceeds budget %d", ErrSemanticFactBudgetExceeded, ord, len(it.Content), maxFactBytes)
			}
			parts = make([]ClientTurnPartShape, 0, len(it.Content))
			for _, cp := range it.Content {
				pBytes := contentPartByteSize(cp)
				totalContentBytes += pBytes
				parts = append(parts, ClientTurnPartShape{
					Kind:         cp.Kind,
					ContentBytes: pBytes,
				})
			}
		}

		shapeItems = append(shapeItems, ClientTurnItemShape{
			Kind:    it.Kind,
			Role:    it.Role,
			Ordinal: int64(ord),
			Parts:   parts,
		})
	}

	shape := ClientTurnShape{
		Items:             shapeItems,
		TotalContentBytes: totalContentBytes,
	}

	if err := shape.Validate(maxFactBytes); err != nil {
		return ClientTurnShape{}, err
	}
	return shape, nil
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
