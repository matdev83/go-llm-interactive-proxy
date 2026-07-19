package comparison_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/comparison"
	"github.com/stretchr/testify/require"
)

func TestRequiredDimensions_coverDesignPhase2(t *testing.T) {
	t.Parallel()
	want := []comparison.Dimension{
		comparison.DimSetup,
		comparison.DimInventory,
		comparison.DimTTFT,
		comparison.DimCompletionLatency,
		comparison.DimPreOutputFailures,
		comparison.DimPostOutputFailures,
		comparison.DimCancellation,
		comparison.DimRestart,
		comparison.DimLeaks,
		comparison.DimContinuity,
		comparison.DimPlatformDefects,
		comparison.DimUpstreamMaintenance,
	}
	require.Equal(t, want, comparison.RequiredDimensions())
}

func TestSyntheticDocument_validatesAndLabels(t *testing.T) {
	t.Parallel()
	doc := comparison.SyntheticDocument()
	require.NoError(t, comparison.ValidateInput(doc))
	rep, err := comparison.BuildReport(doc)
	require.NoError(t, err)
	require.Equal(t, comparison.SchemaVersion, rep.SchemaVersion)
	require.Equal(t, "retain_both_connectors", rep.ReplacementStatus)
	require.Contains(t, rep.ExperimentalNote, "experimental")
	require.Contains(t, rep.ExperimentalNote, "non-default")
	require.NotEmpty(t, rep.Limitations)
	require.Contains(t, strings.Join(rep.Limitations, "\n"), "blocked until")

	var sawSynthetic, sawBlocked bool
	for _, c := range rep.Cells {
		switch c.Evidence {
		case comparison.EvidenceSynthetic:
			sawSynthetic = true
		case comparison.EvidenceBlocked:
			sawBlocked = true
			require.NotEmpty(t, c.BlockedReason)
		case comparison.EvidenceMeasured:
			t.Fatalf("default synthetic fixture must not claim measured: %+v", c)
		}
	}
	require.True(t, sawSynthetic)
	require.True(t, sawBlocked)
	require.Len(t, rep.Coverage, len(comparison.RequiredDimensions())*2)
}

func TestBuildReport_markdownAndJSONSafe(t *testing.T) {
	t.Parallel()
	rep, err := comparison.BuildReport(comparison.SyntheticDocument())
	require.NoError(t, err)

	var jsonBuf bytes.Buffer
	require.NoError(t, comparison.WriteJSON(&jsonBuf, rep))
	require.NotContains(t, jsonBuf.String(), "api_key")
	require.NotContains(t, jsonBuf.String(), "sk-")
	require.NotContains(t, jsonBuf.String(), "/home/")

	var md bytes.Buffer
	require.NoError(t, comparison.WriteMarkdown(&md, rep))
	out := md.String()
	require.Contains(t, out, "| `cursorsdk` |")
	require.Contains(t, out, "| `cursorcliacp` |")
	require.Contains(t, out, "`synthetic`")
	require.Contains(t, out, "`blocked`")
	require.Contains(t, out, "Limitations")
}

func TestValidateInput_requiresFullMatrix(t *testing.T) {
	t.Parallel()
	doc := comparison.SyntheticDocument()
	doc.Cells = doc.Cells[:len(doc.Cells)-1]
	require.Error(t, comparison.ValidateInput(doc))
}

func TestValidateInput_measuredNeedsSamples(t *testing.T) {
	t.Parallel()
	doc := comparison.SyntheticDocument()
	for i := range doc.Cells {
		if doc.Cells[i].Dimension == comparison.DimSetup && doc.Cells[i].Connector == comparison.ConnectorSDK {
			doc.Cells[i].Evidence = comparison.EvidenceMeasured
			doc.Cells[i].Aggregates.Samples = 0
			doc.Cells[i].BlockedReason = ""
		}
	}
	err := comparison.ValidateInput(doc)
	require.Error(t, err)
	require.Contains(t, err.Error(), "samples")
}

func TestDecodeInputJSON_rejectsUnknownFields(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(comparison.SyntheticDocument())
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	m["prompt"] = "should-not-appear"
	bad, err := json.Marshal(m)
	require.NoError(t, err)
	_, err = comparison.DecodeInputJSON(bytes.NewReader(bad))
	require.Error(t, err)
}
