package metrics

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview/sdkadapter"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/nonforwardable"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/steering"
)

// NewConversationViewServicesWithMetrics is the production infra adapter that combines
// a continuity Store (any value satisfying the optional conversation-view capability)
// with the metrics Bundle observer. It constructs the trusted Registrar and Writer
// with bounded mutation diagnostics wired to lip_conversation_view_* series.
// The observer receives only bounded enums (operation, placement), never OverlayID/ALegID/digest/plaintext.
// If bundle is nil, the observer is a no-op (panic-isolated).
// The resolver may be nil if the caller never uses after_ingress_tail; Put with that placement will fail closed.
// This helper is the explicit production composition seam for Req 5.2 without globals or HTTP exposure.
func NewConversationViewServicesWithMetrics(store any, aLegID string, resolver sdkadapter.TrajectoryResolver, b *Bundle) (nonforwardable.Registrar, steering.Writer, error) {
	var obs conversationview.Observer
	if b != nil {
		obs = b.ConversationViewObserver()
	}
	return sdkadapter.NewConversationViewServicesWithObserver(store, aLegID, resolver, obs)
}

// NewSteeringWriterWithMetrics constructs a Writer with metrics observer from a Bundle.
func NewSteeringWriterWithMetrics(store any, aLegID string, resolver sdkadapter.TrajectoryResolver, b *Bundle) (steering.Writer, error) {
	var obs conversationview.Observer
	if b != nil {
		obs = b.ConversationViewObserver()
	}
	return sdkadapter.NewSteeringWriterFromStoreWithObserver(store, aLegID, resolver, obs)
}
