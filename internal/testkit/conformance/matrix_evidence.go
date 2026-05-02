package conformance

// MatrixEvidence documents how FE×BE matrix coverage is enforced in tests.
// Human-readable tables: docs/conformance-matrix-evidence.md

// MatrixEvidenceTextFiles list conformance sources that must iterate AllCells with TextViable.
var MatrixEvidenceTextFiles = []string{
	"conformance_text_test.go",
	"backend_credentials_test.go",
}

// MatrixEvidenceToolsFiles must iterate AllCells with ToolsViable.
var MatrixEvidenceToolsFiles = []string{
	"conformance_tools_test.go",
	"backend_credentials_test.go",
}

// MatrixEvidenceMultimodalFiles must iterate AllCells with MultimodalViable.
var MatrixEvidenceMultimodalFiles = []string{
	"conformance_multimodal_test.go",
	"backend_credentials_test.go",
}
