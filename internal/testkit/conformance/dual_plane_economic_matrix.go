package conformance

// DualPlaneEconomicMode is one customer-economic path exercised across all
// bundled frontends (Phase 7.1 / requirements 1.2, 1.3, 5.1, 5.4–5.7, 13.2, 13.9).
type DualPlaneEconomicMode string

const (
	DualPlaneEconomicModeStream          DualPlaneEconomicMode = "stream"
	DualPlaneEconomicModeNonStream       DualPlaneEconomicMode = "nonstream"
	DualPlaneEconomicModeProtocolError   DualPlaneEconomicMode = "protocol_error"
	DualPlaneEconomicModeCancel          DualPlaneEconomicMode = "cancel"
	DualPlaneEconomicModeEncodingFailure DualPlaneEconomicMode = "encoding_failure"
)

// DualPlaneEconomicModes returns every required dual-plane economic mode in
// stable order.
func DualPlaneEconomicModes() []DualPlaneEconomicMode {
	return []DualPlaneEconomicMode{
		DualPlaneEconomicModeStream,
		DualPlaneEconomicModeNonStream,
		DualPlaneEconomicModeProtocolError,
		DualPlaneEconomicModeCancel,
		DualPlaneEconomicModeEncodingFailure,
	}
}

// IsKnown reports whether m is a documented dual-plane economic mode.
func (m DualPlaneEconomicMode) IsKnown() bool {
	switch m {
	case DualPlaneEconomicModeStream, DualPlaneEconomicModeNonStream,
		DualPlaneEconomicModeProtocolError, DualPlaneEconomicModeCancel,
		DualPlaneEconomicModeEncodingFailure:
		return true
	}
	return false
}

// DualPlaneEconomicCell is one frontend × economic-mode combination.
type DualPlaneEconomicCell struct {
	Frontend string
	Mode     DualPlaneEconomicMode
}

// DualPlaneEconomicCells returns the Cartesian product of bundled frontends and
// dual-plane economic modes (4×5 = 20 cells).
func DualPlaneEconomicCells() []DualPlaneEconomicCell {
	fe := BundledFrontendIDs()
	modes := DualPlaneEconomicModes()
	out := make([]DualPlaneEconomicCell, 0, len(fe)*len(modes))
	for _, f := range fe {
		for _, m := range modes {
			out = append(out, DualPlaneEconomicCell{Frontend: f, Mode: m})
		}
	}
	return out
}
