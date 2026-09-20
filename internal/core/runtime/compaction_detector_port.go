package runtime

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/compactionfacts"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
)

// CompactionDetector is the repository-internal consumer port for session
// compaction detection. It exposes only the four observation operations
// consumed by the core runtime, using canonical lipapi and compaction SDK types.
type CompactionDetector interface {
	PreviewRequest(compaction.PreservationMeta, lipapi.Call) compaction.RequestPreview
	RequestOpened(compaction.PreservationMeta, lipapi.Call) []compaction.Event
	PreviewResponse(compaction.PreservationMeta, lipapi.Event) compaction.ResponsePreview
	ResponseReleased(compaction.PreservationMeta, lipapi.Event) []compaction.Event
}

// CompactionWireDetector extends CompactionDetector with exact-facts request observation
// for wire streaming execution without materializing a full lipapi.Call.
type CompactionWireDetector interface {
	CompactionDetector
	RequestOpenedFacts(compaction.PreservationMeta, compactionfacts.RequestFacts) []compaction.Event
}
