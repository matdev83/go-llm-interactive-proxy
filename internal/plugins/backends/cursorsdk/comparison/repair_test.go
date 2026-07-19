package comparison_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/comparison"
	"github.com/stretchr/testify/require"
)

func TestSyntheticDocument_noComparativeMetrics(t *testing.T) {
	t.Parallel()
	doc := comparison.SyntheticDocument()
	require.NoError(t, comparison.ValidateInput(doc))

	var sdkTTFT, acpTTFT *float64
	for _, c := range doc.Cells {
		require.Nil(t, c.Aggregates.Rate)
		require.Nil(t, c.Aggregates.P50Ms)
		require.Nil(t, c.Aggregates.P95Ms)
		require.Nil(t, c.Aggregates.Count)
		require.Nil(t, c.Aggregates.MaxLive)
		require.Nil(t, c.Aggregates.DurationMs)
		require.Zero(t, c.Aggregates.Samples)
		if c.Evidence == comparison.EvidenceBlocked {
			require.True(t, comparison.ValidBlockedReason(c.BlockedReason))
		}
		if c.Note != "" {
			require.True(t, comparison.ValidNoteCode(c.Note))
		}
		if c.Dimension == comparison.DimTTFT {
			switch c.Connector {
			case comparison.ConnectorSDK:
				sdkTTFT = c.Aggregates.P50Ms
			case comparison.ConnectorACP:
				acpTTFT = c.Aggregates.P50Ms
			}
		}
	}
	require.Nil(t, sdkTTFT)
	require.Nil(t, acpTTFT)
}

func TestValidateInput_blockedRejectsMetrics(t *testing.T) {
	t.Parallel()
	doc := comparison.SyntheticDocument()
	rate := 0.0
	for i := range doc.Cells {
		if doc.Cells[i].Evidence == comparison.EvidenceBlocked {
			doc.Cells[i].Aggregates.Rate = &rate
			break
		}
	}
	err := comparison.ValidateInput(doc)
	require.Error(t, err)
	require.Contains(t, err.Error(), "blocked")
}

func TestValidateInput_syntheticRejectsLatency(t *testing.T) {
	t.Parallel()
	doc := comparison.SyntheticDocument()
	p50 := 120.0
	for i := range doc.Cells {
		if doc.Cells[i].Evidence == comparison.EvidenceSynthetic {
			doc.Cells[i].Aggregates.P50Ms = &p50
			break
		}
	}
	err := comparison.ValidateInput(doc)
	require.Error(t, err)
	require.Contains(t, err.Error(), "synthetic")
}

func TestWriteMarkdown_usesDashNotZeroMetrics(t *testing.T) {
	t.Parallel()
	rep, err := comparison.BuildReport(comparison.SyntheticDocument())
	require.NoError(t, err)
	var md bytes.Buffer
	require.NoError(t, comparison.WriteMarkdown(&md, rep))
	out := md.String()
	require.Contains(t, out, "| `-` |")
	require.NotContains(t, out, "| 0 |")
	require.NotContains(t, out, "120")
	require.NotContains(t, out, "140")
}

func TestBuildReport_scansHandBuiltDocument(t *testing.T) {
	t.Parallel()
	doc := comparison.SyntheticDocument()
	doc.Cells[0].Note = "/opt/secret-proj"
	_, err := comparison.BuildReport(doc)
	require.Error(t, err)
}

func TestScanForbidden_adversarialPathsNested(t *testing.T) {
	t.Parallel()
	base, err := json.Marshal(comparison.SyntheticDocument())
	require.NoError(t, err)

	cases := map[string]string{
		"opt_unix":  "/opt/secret-proj/foo",
		"tmp_unix":  "/tmp/ws",
		"win_drive": `C:\Users\mate\ws`,
		"win_slash": `D:/data/repo`,
		"unc":       `\\server\share\path`,
		"file_uri":  "file:///tmp/ws",
		"file_win":  "file:///C:/Users/mate/ws",
		"sk":        "sk-abcdefghijklmnopqrstuv",
		"crsr":      "crsr_abcdefghijklmnop",
		"agent_id":  "agent-abcdef123456",
		"run_id":    "run-abcdef123456",
		"prompt_m":  "prompt=hello",
		"tool_args": "tool_arguments={}",
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var m map[string]any
			require.NoError(t, json.Unmarshal(base, &m))
			firstCell(t, m)["note"] = bad
			raw, err := json.Marshal(m)
			require.NoError(t, err)
			require.Error(t, comparison.ScanForbiddenRawJSON(raw), "expected reject %s", name)
		})
	}
}

func TestLoadInputBytes_rejectsOversize(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(comparison.SyntheticDocument())
	require.NoError(t, err)
	pad := bytes.Repeat([]byte(" "), comparison.MaxInputBytes+1)
	_, err = comparison.LoadInputBytes(append(raw, pad...))
	require.Error(t, err)
	require.Contains(t, err.Error(), "size")
}

func TestValidateInput_rejectsFreeProseNote(t *testing.T) {
	t.Parallel()
	doc := comparison.SyntheticDocument()
	doc.Cells[0].Note = "see the workspace later"
	require.Error(t, comparison.ValidateInput(doc))
}

func TestWriteJSON_omitsNilMetrics(t *testing.T) {
	t.Parallel()
	rep, err := comparison.BuildReport(comparison.SyntheticDocument())
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, comparison.WriteJSON(&buf, rep))
	require.NotContains(t, buf.String(), `"rate"`)
	require.NotContains(t, buf.String(), `"p50_ms"`)
	require.NotContains(t, buf.String(), `"count"`)
	require.True(t, strings.Contains(buf.String(), `"samples": 0`))
}
