package protocol_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/stretchr/testify/require"
)

func TestPinnedSDKContractFixture(t *testing.T) {
	t.Parallel()
	var contract struct {
		SDKVersion           string         `json:"sdkVersion"`
		NodeEngine           string         `json:"nodeEngine"`
		Platforms            []string       `json:"platforms"`
		UnsupportedPlatforms []string       `json:"unsupportedPlatforms"`
		APIs                 []string       `json:"apis"`
		DeltaKinds           []string       `json:"deltaKinds"`
		Excluded             []string       `json:"excluded"`
		DefaultsToOverride   map[string]any `json:"defaultsToOverride"`
	}
	require.NoError(t, protocol.DecodeFixtureJSON("sdk_contract.json", &contract))
	require.Equal(t, protocol.PinnedSDKVersion, contract.SDKVersion)
	require.Equal(t, protocol.MinNodeEngine, contract.NodeEngine)
	require.Contains(t, contract.APIs, "Cursor.models.list")
	require.Contains(t, contract.APIs, "Agent.create")
	require.Contains(t, contract.APIs, "Run.cancel")
	require.Contains(t, contract.DeltaKinds, "text-delta")
	require.Contains(t, contract.DeltaKinds, "thinking-delta")
	require.Contains(t, contract.Excluded, "Agent.resume")
	require.Equal(t, false, contract.DefaultsToOverride["enableAgentRetries"])
	require.Contains(t, contract.UnsupportedPlatforms, "win32-arm64")
}

func TestSanitizedModelFixturesPreserveDistinctions(t *testing.T) {
	t.Parallel()
	var fixture struct {
		SDKVersion string `json:"sdkVersion"`
		Models     []struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
			Parameters  []struct {
				ID     string   `json:"id"`
				Type   string   `json:"type"`
				Values []string `json:"values"`
			} `json:"parameters"`
			Variants []struct {
				ID     string         `json:"id"`
				Params map[string]any `json:"params"`
			} `json:"variants"`
		} `json:"models"`
		Distinctions map[string]string `json:"distinctions"`
	}
	require.NoError(t, protocol.DecodeFixtureJSON("models_sanitized.json", &fixture))
	require.Equal(t, protocol.PinnedSDKVersion, fixture.SDKVersion)

	byID := map[string]int{}
	for i, m := range fixture.Models {
		require.NotEmpty(t, m.ID)
		require.NotEmpty(t, m.DisplayName)
		byID[m.ID] = i
	}

	reasoningID := fixture.Distinctions["reasoningParam"]
	effortID := fixture.Distinctions["effortPlusThinking"]
	boolOnlyID := fixture.Distinctions["booleanThinkingOnly"]
	require.Contains(t, byID, reasoningID)
	require.Contains(t, byID, effortID)
	require.Contains(t, byID, boolOnlyID)

	reasoning := fixture.Models[byID[reasoningID]]
	require.True(t, hasParamValues(reasoning.Parameters, "reasoning", "xhigh"))
	require.False(t, hasParamValues(reasoning.Parameters, "reasoning", "extra-high"))

	effort := fixture.Models[byID[effortID]]
	require.True(t, hasParam(effort.Parameters, "thinking"))
	require.True(t, hasParamValues(effort.Parameters, "effort", "extra-high"))
	require.False(t, hasParamValues(effort.Parameters, "effort", "xhigh"))
	require.True(t, hasVariantParam(effort.Variants, "thinking", true))
	require.True(t, hasVariantParam(effort.Variants, "effort", "extra-high"))

	boolOnly := fixture.Models[byID[boolOnlyID]]
	require.True(t, hasParam(boolOnly.Parameters, "thinking"))
	require.False(t, hasParam(boolOnly.Parameters, "reasoning"))
	require.False(t, hasParam(boolOnly.Parameters, "effort"))

	require.Equal(t, "xhigh", fixture.Distinctions["xhighValue"])
	require.Equal(t, "extra-high", fixture.Distinctions["extraHighValue"])
	require.NotEqual(t, fixture.Distinctions["xhighValue"], fixture.Distinctions["extraHighValue"])
}

func TestMethodsFixtureMatchesConstants(t *testing.T) {
	t.Parallel()
	var fixture struct {
		SchemaVersion int      `json:"schemaVersion"`
		MaxFrameBytes int      `json:"maxFrameBytes"`
		Methods       []string `json:"methods"`
		EventKinds    []string `json:"eventKinds"`
		TerminalKinds []string `json:"terminalEventKinds"`
	}
	require.NoError(t, protocol.DecodeFixtureJSON("protocol/methods.json", &fixture))
	require.Equal(t, protocol.SchemaVersion, fixture.SchemaVersion)
	require.Equal(t, protocol.MaxFrameBytes, fixture.MaxFrameBytes)
	require.Equal(t, protocol.RequiredMethods(), fixture.Methods)
	require.Equal(t, protocol.EventKinds(), fixture.EventKinds)
	for _, kind := range fixture.TerminalKinds {
		require.True(t, protocol.IsTerminalKind(kind))
	}
}

func TestValidFramesFixtureRoundTrip(t *testing.T) {
	t.Parallel()
	raw := mustReadFixture(t, "protocol/valid_frames.ndjson")
	lines := splitNonEmptyLines(raw)
	require.NotEmpty(t, lines)

	pending := map[string]*protocol.Frame{}
	seq := protocol.NewRunSequencer()
	for _, line := range lines {
		f, err := protocol.DecodeLine(line)
		require.NoError(t, err, "line=%s", line)
		encoded, err := protocol.EncodeFrame(f)
		require.NoError(t, err)
		again, err := protocol.DecodeLine(encoded)
		require.NoError(t, err)
		require.Equal(t, f.Type, again.Type)
		require.Equal(t, f.ID, again.ID)
		require.Equal(t, f.Method, again.Method)
		require.Equal(t, f.RunID, again.RunID)
		require.Equal(t, f.Kind, again.Kind)

		switch f.Type {
		case protocol.TypeRequest:
			pending[f.ID] = f
		case protocol.TypeResponse:
			req := pending[f.ID]
			require.NotNil(t, req, "orphan response %s", f.ID)
			require.NoError(t, protocol.MatchResponse(req, f))
			delete(pending, f.ID)
		case protocol.TypeEvent:
			require.NoError(t, seq.Accept(f))
		}
	}
}

func TestInvalidFramesFixture(t *testing.T) {
	t.Parallel()
	var fixture struct {
		Cases []struct {
			Name       string `json:"name"`
			Raw        string `json:"raw"`
			ErrorClass string `json:"errorClass"`
		} `json:"cases"`
	}
	require.NoError(t, protocol.DecodeFixtureJSON("protocol/invalid_frames.json", &fixture))
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			raw := tc.Raw
			if raw == "__OVERSIZE__" {
				raw = `{"schemaVersion":1,"type":"request","id":"r","method":"bridge/health","params":{"pad":"` + strings.Repeat("x", protocol.MaxFrameBytes) + `"}}`
			}
			_, err := protocol.DecodeLineString(raw)
			require.Error(t, err)
			var pe *protocol.ProtocolError
			require.ErrorAs(t, err, &pe)
			require.Equal(t, tc.ErrorClass, pe.Class)
		})
	}
}

func TestEventSequenceFixture(t *testing.T) {
	t.Parallel()
	var fixture struct {
		Cases []struct {
			Name   string            `json:"name"`
			Expect string            `json:"expect"`
			Events []json.RawMessage `json:"events"`
		} `json:"cases"`
	}
	require.NoError(t, protocol.DecodeFixtureJSON("protocol/event_sequences.json", &fixture))
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			seq := protocol.NewRunSequencer()
			var lastErr error
			for _, raw := range tc.Events {
				f, err := protocol.DecodeLine(raw)
				require.NoError(t, err)
				lastErr = seq.Accept(f)
				if lastErr != nil {
					break
				}
			}
			if tc.Expect == "ok" {
				require.NoError(t, lastErr)
				require.True(t, seq.Terminated("run-1"))
				return
			}
			require.Error(t, lastErr)
			var pe *protocol.ProtocolError
			require.ErrorAs(t, lastErr, &pe)
			require.Equal(t, tc.Expect, pe.Class)
		})
	}
}

func TestUnknownOptionalFieldsIgnored(t *testing.T) {
	t.Parallel()
	line := `{"schemaVersion":1,"type":"request","id":"r1","method":"bridge/health","params":{},"futureField":{"x":1},"extra":"ok"}`
	f, err := protocol.DecodeLineString(line)
	require.NoError(t, err)
	require.Equal(t, "r1", f.ID)
	require.Equal(t, protocol.MethodHealth, f.Method)
}

func TestWriteFrameAddsNewline(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	f := &protocol.Frame{
		SchemaVersion: protocol.SchemaVersion,
		Type:          protocol.TypeRequest,
		ID:            "r1",
		Method:        protocol.MethodHealth,
		Params:        json.RawMessage(`{}`),
	}
	require.NoError(t, protocol.WriteFrame(&buf, f))
	require.True(t, strings.HasSuffix(buf.String(), "\n"))
	decoded, err := protocol.DecodeLineString(strings.TrimSuffix(buf.String(), "\n"))
	require.NoError(t, err)
	require.Equal(t, "r1", decoded.ID)
}

type shortWriter struct {
	n   int
	err error
	got []byte
}

func (w *shortWriter) Write(p []byte) (int, error) {
	w.got = append(w.got, p...)
	if w.n < len(p) {
		return w.n, w.err
	}
	return len(p), nil
}

func TestWriteFrame_SingleWriteShortWriteError(t *testing.T) {
	t.Parallel()
	f := &protocol.Frame{
		SchemaVersion: protocol.SchemaVersion,
		Type:          protocol.TypeRequest,
		ID:            "r1",
		Method:        protocol.MethodHealth,
		Params:        json.RawMessage(`{}`),
	}
	raw, err := protocol.EncodeFrame(f)
	require.NoError(t, err)
	wantLen := len(raw) + 1
	w := &shortWriter{n: 3, err: io.ErrShortWrite}
	err = protocol.WriteFrame(w, f)
	require.ErrorIs(t, err, io.ErrShortWrite)
	require.Len(t, w.got, wantLen, "must attempt one framed write including newline")
	require.Equal(t, byte('\n'), w.got[len(w.got)-1])
}

func TestFixturesContainNoSecretMaterial(t *testing.T) {
	t.Parallel()
	root := mustFixtureRoot(t)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		lower := strings.ToLower(string(raw))
		require.NotContains(t, lower, "sk-")
		require.NotContains(t, lower, "cursor_api_key=")
		require.NotContains(t, string(raw), "-----BEGIN")
		return nil
	})
	require.NoError(t, err)
}

func hasParam(params []struct {
	ID     string   `json:"id"`
	Type   string   `json:"type"`
	Values []string `json:"values"`
}, id string,
) bool {
	for _, p := range params {
		if p.ID == id {
			return true
		}
	}
	return false
}

func hasParamValues(params []struct {
	ID     string   `json:"id"`
	Type   string   `json:"type"`
	Values []string `json:"values"`
}, id, value string,
) bool {
	for _, p := range params {
		if p.ID != id {
			continue
		}
		return slices.Contains(p.Values, value)
	}
	return false
}

func hasVariantParam(variants []struct {
	ID     string         `json:"id"`
	Params map[string]any `json:"params"`
}, key string, want any,
) bool {
	for _, v := range variants {
		got, ok := v.Params[key]
		if !ok {
			continue
		}
		if got == want {
			return true
		}
	}
	return false
}

func splitNonEmptyLines(raw []byte) [][]byte {
	parts := bytes.Split(raw, []byte("\n"))
	out := make([][]byte, 0, len(parts))
	for _, p := range parts {
		p = bytes.TrimSpace(p)
		if len(p) == 0 {
			continue
		}
		out = append(out, p)
	}
	return out
}
