package conformance

// FE×BE conformance matrix for bundled plugins. Protocol-specific parity suites live in
// parity_*.go in this package; row IDs and scope live in .kiro/specs/llm-api-parity/design.md.

// BundledFrontendIDs is the authoritative list of v1 bundled frontend protocol IDs (Requirement 15.12).
// When adding a frontend, extend this slice and add matrix rows for every backend in BundledBackendIDs.
func BundledFrontendIDs() []string {
	return []string{
		"openai-responses",
		"openai-legacy",
		"anthropic",
		"gemini",
	}
}

// BundledBackendIDs is the authoritative list of v1 bundled backend connector IDs (Requirement 15.12).
func BundledBackendIDs() []string {
	return []string{
		"openai-responses",
		"openai-legacy",
		"anthropic",
		"gemini",
		"bedrock",
		"acp",
	}
}

// SubsetMeta records which conformance rows apply to a matrix cell.
type SubsetMeta struct {
	TextViable          bool
	ToolsViable         bool
	MultimodalViable    bool
	SubsetJustification string // non-empty when any row is intentionally limited or deferred
}

// MatrixCell is one frontend × backend combination (24 total).
type MatrixCell struct {
	Frontend string
	Backend  string
	Meta     SubsetMeta
}

// AllCells returns the full Cartesian product with explicit subset metadata (Tasks 12.0, 12.3 design footnote).
func AllCells() []MatrixCell {
	fe := BundledFrontendIDs()
	be := BundledBackendIDs()
	out := make([]MatrixCell, 0, len(fe)*len(be))
	for _, f := range fe {
		for _, b := range be {
			out = append(out, newCell(f, b))
		}
	}
	return out
}

func newCell(frontend, backend string) MatrixCell {
	meta := SubsetMeta{
		TextViable:       true,
		ToolsViable:      true,
		MultimodalViable: true,
	}
	switch backend {
	case "acp":
		meta.ToolsViable = false
		meta.MultimodalViable = false
		meta.SubsetJustification = "ACP v1 prompt-turn subset rejects canonical tools (validateACPCall); multimodal matrix rows for FE×ACP are deferred per design.md conformance table footnote (Tasks 12.2–12.3 use text-only + tool-exclusion documentation for this column)."
	default:
		meta.SubsetJustification = ""
	}
	return MatrixCell{Frontend: frontend, Backend: backend, Meta: meta}
}
