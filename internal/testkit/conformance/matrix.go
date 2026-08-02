package conformance

// FE×BE conformance matrix for bundled plugins. Protocol-specific parity suites live in
// parity_*.go in this package; row IDs and scope live in .kiro/specs/llm-api-parity/design.md
// and .kiro/specs/openresponses-api-support/design.md (authoritative 5×9 matrix).

// BundledFrontendIDs is the authoritative list of v1 bundled frontend protocol IDs
// (Requirement 13.5). When adding a frontend, extend this slice and add matrix rows for
// every backend in BundledBackendIDs.
func BundledFrontendIDs() []string {
	return []string{
		"openai-responses",
		"openai-legacy",
		"anthropic",
		"gemini",
		"openresponses",
	}
}

// BundledBackendIDs is the authoritative list of v1 bundled backend compatibility
// identities (Requirement 13.5): the six essential backends, the generic OpenResponses
// backend, and the OpenRouter/NVIDIA provider-connector identities. OpenRouter and NVIDIA
// are authoritative compatibility identities but remain optional connectors; their matrix
// cells are driven through the classified configured provider-mode path
// (DeployConfiguredProviderMode / the openai-compat provider-mode backend) and are never
// promoted to essential backend kinds.
func BundledBackendIDs() []string {
	return []string{
		"openai-responses",
		"openai-legacy",
		"anthropic",
		"gemini",
		"bedrock",
		"acp",
		"openresponses",
		"openrouter",
		"nvidia",
	}
}

// MatrixCellDriver classifies how one matrix cell is constructed and driven. Every cell
// must have exactly one driver; cells without a working construct/mount helper or a
// classified configured-mode path fail the matrix completeness test.
type MatrixCellDriver string

const (
	// DriverBase is the base-bundle construct/mount path: a real essential/OpenResponses
	// backend adapter behind the real core executor and the real frontend handler, with an
	// injectable reference-provider origin.
	DriverBase MatrixCellDriver = "base"
	// DriverConfiguredProviderMode is the classified configured-mode path for the
	// OpenRouter/NVIDIA optional-connector columns: the real frontend behind the real core
	// executor and the configured OpenAI-compatible provider-mode backend
	// (custom-openai-responses-compatible) reaching an OpenAI-compatible provider origin.
	// The connectors themselves stay optional and are never constructed as essential
	// backends (AssertOpenRouterNVIDIAStayOptional).
	DriverConfiguredProviderMode MatrixCellDriver = "configured_provider_mode"
)

// SubsetMeta records which conformance rows apply to a matrix cell.
type SubsetMeta struct {
	TextViable          bool
	ToolsViable         bool
	MultimodalViable    bool
	SubsetJustification string // non-empty when any row is intentionally limited or deferred
}

// MatrixCell is one frontend × backend compatibility identity (45 total).
type MatrixCell struct {
	Frontend string
	Backend  string
	Driver   MatrixCellDriver
	Meta     SubsetMeta
}

// AllCells returns the full Cartesian product with explicit subset metadata and a
// classified driver for every identity (Tasks 12.0, 12.3, 13.5).
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
		meta.MultimodalViable = true
		meta.SubsetJustification = "ACP v1 prompt-turn subset rejects canonical tools (validateACPCall); " +
			"multimodal image/file URI references project to ACP resource prompt blocks (proven by the " +
			"executable multimodal scenarios and the OpenResponses row resource-subset evidence), while " +
			"unrepresentable multimodal forms (video/audio) are rejected before network."
	case "openrouter", "nvidia":
		meta.SubsetJustification = "OpenRouter/NVIDIA are optional provider-connector compatibility " +
			"identities driven through the classified configured OpenAI-compatible provider-mode path " +
			"(DeployConfiguredProviderMode); they are not constructible as essential bundled backends and " +
			"are never promoted to essential status (AssertOpenRouterNVIDIAStayOptional)."
	default:
		meta.SubsetJustification = ""
	}
	return MatrixCell{
		Frontend: frontend,
		Backend:  backend,
		Driver:   driverForBackend(backend),
		Meta:     meta,
	}
}

// GeneralMatrixCells returns the authoritative matrix cells that belong to
// neither the OpenResponses frontend row (FE=openresponses) nor the OpenResponses
// backend column (BE=openresponses). The full Cartesian product has 5×9 = 45
// cells; the excluded union is 9 + 5 − 1 = 13 (the openresponses×openresponses
// cell is the single overlap between row and column), so the general cells are
// the remaining 4×8 = 32 cells.
func GeneralMatrixCells() []MatrixCell {
	out := make([]MatrixCell, 0, 32)
	for _, cell := range AllCells() {
		if cell.Frontend == FrontendOpenResponses || cell.Backend == BackendOpenResponses {
			continue
		}
		out = append(out, cell)
	}
	return out
}

// driverForBackend classifies the construct/mount driver for one backend compatibility
// identity: base-bundle construction for the six essential backends and the generic
// OpenResponses backend; the configured provider-mode path for the optional connector
// columns.
func driverForBackend(backend string) MatrixCellDriver {
	switch backend {
	case "openrouter", "nvidia":
		return DriverConfiguredProviderMode
	default:
		return DriverBase
	}
}
