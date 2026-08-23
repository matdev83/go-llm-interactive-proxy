package sdkadapter

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/nonforwardable"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/steering"
)

// NewNonforwardableRegistrarFromStore resolves the optional conversation-view
// Tagger capability from v and constructs a registrar. It fails deterministically
// if the capability is unavailable (Req 13.4) rather than running without
// safety/persistence.
func NewNonforwardableRegistrarFromStore(v any) (nonforwardable.Registrar, error) {
	tagger, ok := conversationview.AsTagger(v)
	if !ok || tagger == nil {
		return nil, fmt.Errorf("sdkadapter: conversation-view tagger capability not available")
	}
	return NewRegistrar(tagger)
}

// NewSteeringWriterFromStore resolves the optional SteeringStore capability from
// v and constructs a writer bound to aLegID. It fails deterministically if the
// capability is unavailable.
func NewSteeringWriterFromStore(v any, aLegID string, resolver TrajectoryResolver) (steering.Writer, error) {
	st, ok := conversationview.AsSteeringStore(v)
	if !ok || st == nil {
		return nil, fmt.Errorf("sdkadapter: conversation-view steering capability not available")
	}
	return NewWriter(st, aLegID, resolver)
}

// NewConversationViewServices is a convenience helper that resolves both
// registrar and writer capabilities from the same store value. It returns
// deterministic errors if either capability is unavailable. Callers that only
// need one capability should use the more narrow constructors above.
func NewConversationViewServices(v any, writerALegID string, resolver TrajectoryResolver) (nonforwardable.Registrar, steering.Writer, error) {
	tagger, ok := conversationview.AsTagger(v)
	if !ok || tagger == nil {
		return nil, nil, fmt.Errorf("sdkadapter: conversation-view tagger capability not available")
	}
	st, ok := conversationview.AsSteeringStore(v)
	if !ok || st == nil {
		return nil, nil, fmt.Errorf("sdkadapter: conversation-view steering capability not available")
	}
	reg, err := NewRegistrar(tagger)
	if err != nil {
		return nil, nil, err
	}
	wr, err := NewWriter(st, writerALegID, resolver)
	if err != nil {
		return nil, nil, err
	}
	return reg, wr, nil
}
