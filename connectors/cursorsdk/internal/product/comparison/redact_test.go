package comparison_test

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/comparison"
	"github.com/stretchr/testify/require"
)

func TestScanForbiddenRawJSON_rejectsSecretsPathsToolsAndIDs(t *testing.T) {
	t.Parallel()
	base, err := json.Marshal(comparison.SyntheticDocument())
	require.NoError(t, err)
	require.NoError(t, comparison.ScanForbiddenRawJSON(base))

	cases := []struct {
		name string
		mut  func(t *testing.T, m map[string]any)
	}{
		{"api_key_field", func(t *testing.T, m map[string]any) {
			t.Helper()
			m["api_key"] = "x"
		}},
		{"nested_prompt", func(t *testing.T, m map[string]any) {
			t.Helper()
			firstCell(t, m)["prompt"] = "hello"
		}},
		{"tool_result", func(t *testing.T, m map[string]any) {
			t.Helper()
			firstCell(t, m)["tool_result"] = "{}"
		}},
		{"unix_path_in_note", func(t *testing.T, m map[string]any) {
			t.Helper()
			firstCell(t, m)["note"] = "see /home/user/project"
		}},
		{"win_path_in_note", func(t *testing.T, m map[string]any) {
			t.Helper()
			firstCell(t, m)["note"] = `see C:\Users\mate\ws`
		}},
		{"key_token", func(t *testing.T, m map[string]any) {
			t.Helper()
			firstCell(t, m)["note"] = "CURSOR_API_KEY=supersecretvalue"
		}},
		{"agent_id", func(t *testing.T, m map[string]any) {
			t.Helper()
			firstCell(t, m)["note"] = "agent-abcdef123456"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var m map[string]any
			require.NoError(t, json.Unmarshal(base, &m))
			tc.mut(t, m)
			raw, err := json.Marshal(m)
			require.NoError(t, err)
			err = comparison.ScanForbiddenRawJSON(raw)
			require.Error(t, err, "expected rejection for %s", tc.name)
		})
	}
}

func firstCell(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	cells, ok := m["cells"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, cells)
	cell, ok := cells[0].(map[string]any)
	require.True(t, ok)
	return cell
}

func TestLoadInputBytes_acceptsSyntheticFixture(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(comparison.SyntheticDocument())
	require.NoError(t, err)
	doc, err := comparison.LoadInputBytes(raw)
	require.NoError(t, err)
	require.NoError(t, comparison.ValidateInput(doc))
}
