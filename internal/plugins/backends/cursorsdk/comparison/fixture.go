package comparison

import (
	"fmt"
	"time"
)

// SyntheticDocument returns a complete matrix for offline/default runs.
// Synthetic cells carry samples=0 with no numeric metrics (no comparative latency).
// Live-credential lanes are blocked with samples=0 and no metrics.
func SyntheticDocument() InputDocument {
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	cells := make([]Cell, 0, len(RequiredDimensions())*2)
	for _, conn := range []ConnectorID{ConnectorSDK, ConnectorACP} {
		for _, dim := range RequiredDimensions() {
			cells = append(cells, syntheticCell(conn, dim))
		}
	}
	return InputDocument{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   now,
		Cells:         cells,
	}
}

func syntheticCell(conn ConnectorID, dim Dimension) Cell {
	switch dim {
	case DimTTFT, DimCompletionLatency:
		return Cell{
			Connector: conn,
			Dimension: dim,
			Evidence:  EvidenceSynthetic,
			Aggregates: Aggregates{
				Samples: 0,
			},
			Incident: IncidentNone,
			Note:     NoteOfflineScaffold,
		}
	case DimSetup, DimInventory, DimCancellation, DimRestart, DimLeaks, DimContinuity,
		DimPreOutputFailures, DimPostOutputFailures, DimPlatformDefects, DimUpstreamMaintenance:
		return Cell{
			Connector: conn,
			Dimension: dim,
			Evidence:  EvidenceBlocked,
			Aggregates: Aggregates{
				Samples: 0,
			},
			Incident:      IncidentPlatformBlocked,
			BlockedReason: blockedReason(conn, dim),
			Note:          NoteAwaitingOptIn,
		}
	default:
		panic(fmt.Sprintf("unhandled dimension %s", dim))
	}
}

func blockedReason(conn ConnectorID, dim Dimension) BlockedReason {
	switch {
	case conn == ConnectorSDK && (dim == DimSetup || dim == DimInventory || dim == DimTTFT || dim == DimCompletionLatency):
		return BlockedSDKLiveOptIn
	case conn == ConnectorACP:
		return BlockedACPDogfoodLane
	default:
		return BlockedMeasuredInputMissing
	}
}
