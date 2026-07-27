package fakebridge_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/fakebridge"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
	"github.com/stretchr/testify/require"
)

func TestFakeBridgeSubprocessLifecycle(t *testing.T) {
	exe := buildFakeBridge(t)

	t.Run("startup_initialize_health_shutdown", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		stdout, stderr, code := runFakeBridge(ctx, t, exe, "", []string{
			reqLine("1", protocol.MethodInitialize, `{"implVersion":"test"}`),
			reqLine("2", protocol.MethodHealth, `{}`),
			reqLine("3", protocol.MethodBridgeShutdown, `{}`),
		})
		require.Equal(t, 0, code, "stderr=%s", stderr)
		frames := decodeSubprocessFrames(t, stdout)
		require.Len(t, frames, 3)
		require.Equal(t, "1", frames[0].ID)
		require.Contains(t, string(frames[0].Result), `"capabilities"`)
		var init protocol.InitializeResult
		require.NoError(t, json.Unmarshal(frames[0].Result, &init))
		require.Equal(t, protocol.RequiredMethods(), init.Capabilities)
		require.Equal(t, "3", frames[2].ID)
		require.Contains(t, string(frames[2].Result), `"shutdown":true`)
	})

	t.Run("models_and_events", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		stdout, stderr, code := runFakeBridge(ctx, t, exe, "", []string{
			reqLine("m1", protocol.MethodModelsList, `{"apiKey":"REDACTED"}`),
			reqLine("s1", protocol.MethodAgentSend, `{"agentId":"agent-1","prompt":"hi"}`),
			reqLine("d1", protocol.MethodBridgeShutdown, `{}`),
		})
		require.Equal(t, 0, code, "stderr=%s", stderr)
		frames := decodeSubprocessFrames(t, stdout)
		require.GreaterOrEqual(t, len(frames), 4)
		require.Contains(t, string(frames[0].Result), "gpt-5.3-codex")
		require.Equal(t, protocol.TypeResponse, frames[1].Type)
		require.Equal(t, protocol.TypeEvent, frames[2].Type)
		require.Equal(t, protocol.KindTextDelta, frames[2].Kind)
	})

	t.Run("malformed_oversized_out_of_order", func(t *testing.T) {
		script := fakebridge.Script{
			OnStartup: []fakebridge.Action{
				{Type: fakebridge.ActionMalformed, Line: "{bad"},
				{Type: fakebridge.ActionOversized}, // Bytes<=0 => MaxFrameBytes+1
			},
			OnMethod: map[string][]fakebridge.Action{
				protocol.MethodAgentSend: {
					{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"run-1"}`)},
					{Type: fakebridge.ActionOutOfOrderEvents},
				},
			},
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		stdout, stderr, code := runFakeBridge(ctx, t, exe, mustJSON(t, script), []string{
			reqLine("1", protocol.MethodAgentSend, `{"agentId":"a","prompt":"p"}`),
			reqLine("2", protocol.MethodBridgeShutdown, `{}`),
		})
		require.Equal(t, 0, code, "stderr=%s", stderr)
		require.Contains(t, stdout, "{bad")

		var oversizedLine []byte
		sc := bufio.NewScanner(strings.NewReader(stdout))
		sc.Buffer(make([]byte, 64*1024), protocol.MaxFrameBytes+4096)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) > protocol.MaxFrameBytes {
				oversizedLine = append([]byte(nil), line...)
				break
			}
		}
		require.NoError(t, sc.Err())
		require.NotEmpty(t, oversizedLine)
		require.Greater(t, len(oversizedLine), protocol.MaxFrameBytes)
		_, err := protocol.DecodeLine(oversizedLine)
		var pe *protocol.ProtocolError
		require.ErrorAs(t, err, &pe)
		require.Equal(t, protocol.ErrorFrameTooLarge, pe.Class)

		frames := decodeSubprocessFrames(t, stdout)
		var events []*protocol.Frame
		for _, f := range frames {
			if f.Type == protocol.TypeEvent && f.Seq != nil {
				events = append(events, f)
			}
		}
		require.GreaterOrEqual(t, len(events), 2)
		require.EqualValues(t, 2, *events[len(events)-2].Seq)
		require.EqualValues(t, 1, *events[len(events)-1].Seq)
	})

	t.Run("blocked_cancel", func(t *testing.T) {
		script := fakebridge.DefaultScript()
		script.OnStartup = []fakebridge.Action{{Type: fakebridge.ActionBlockCancel}}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		stdout, stderr, code := runFakeBridge(ctx, t, exe, mustJSON(t, script), []string{
			reqLine("c1", protocol.MethodRunCancel, `{"runId":"run-1"}`),
			reqLine("h1", protocol.MethodHealth, `{}`),
			reqLine("s1", protocol.MethodBridgeShutdown, `{}`),
		})
		require.Equal(t, 0, code)
		require.Contains(t, stderr, "cancel blocked")
		frames := decodeSubprocessFrames(t, stdout)
		ids := make([]string, 0, len(frames))
		for _, f := range frames {
			ids = append(ids, f.ID)
		}
		require.NotContains(t, ids, "c1")
		require.Contains(t, ids, "h1")
		require.Contains(t, ids, "s1")
	})

	t.Run("bounded_stderr", func(t *testing.T) {
		script := fakebridge.Script{
			OnStartup: []fakebridge.Action{
				{Type: fakebridge.ActionStderr, Text: "diag-line"},
			},
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, stderr, code := runFakeBridge(ctx, t, exe, mustJSON(t, script), []string{
			reqLine("1", protocol.MethodBridgeShutdown, `{}`),
		})
		require.Equal(t, 0, code)
		require.Contains(t, stderr, "diag-line")
		require.Less(t, len(stderr), 4096)
	})

	t.Run("exit_code", func(t *testing.T) {
		script := fakebridge.Script{
			OnStartup: []fakebridge.Action{{Type: fakebridge.ActionExit, Code: 7}},
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _, code := runFakeBridge(ctx, t, exe, mustJSON(t, script), nil)
		require.Equal(t, 7, code)
	})

	t.Run("ignore_shutdown", func(t *testing.T) {
		script := fakebridge.DefaultScript()
		script.OnStartup = []fakebridge.Action{{Type: fakebridge.ActionIgnoreShutdown}}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		stdout, stderr, code := runFakeBridge(ctx, t, exe, mustJSON(t, script), []string{
			reqLine("1", protocol.MethodBridgeShutdown, `{}`),
			reqLine("2", protocol.MethodHealth, `{}`),
		})
		require.Equal(t, 0, code)
		require.Contains(t, stderr, "shutdown ignored")
		frames := decodeSubprocessFrames(t, stdout)
		require.Len(t, frames, 1)
		require.Equal(t, "2", frames[0].ID)
	})
}

func buildFakeBridge(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "fake-cursor-sdk-bridge"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	exe := filepath.Join(dir, name)
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	pkgDir := filepath.Dir(thisFile)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", exe, "./cmd/fake-cursor-sdk-bridge")
	cmd.Dir = pkgDir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "go build failed: %s", out)
	return exe
}

func runFakeBridge(ctx context.Context, t *testing.T, exe, scriptJSON string, lines []string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.CommandContext(ctx, exe)
	cmd.Env = append(os.Environ(), "FAKE_BRIDGE_SCRIPT="+scriptJSON)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	require.NoError(t, cmd.Start())

	writeDone := make(chan error, 1)
	go func() {
		defer func() {
			if err := stdin.Close(); err != nil {
				select {
				case writeDone <- err:
				default:
				}
			}
		}()
		for _, line := range lines {
			if _, err := io.WriteString(stdin, line+"\n"); err != nil {
				writeDone <- err
				return
			}
		}
		writeDone <- nil
	}()
	select {
	case err := <-writeDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatalf("stdin write timed out: %v", ctx.Err())
	}

	waitErr := cmd.Wait()
	stdout = stdoutBuf.String()
	stderr = stderrBuf.String()
	if waitErr == nil {
		return stdout, stderr, 0
	}
	var ee *exec.ExitError
	require.ErrorAs(t, waitErr, &ee)
	return stdout, stderr, ee.ExitCode()
}

func reqLine(id, method, params string) string {
	f := &protocol.Frame{
		SchemaVersion: protocol.SchemaVersion,
		Type:          protocol.TypeRequest,
		ID:            id,
		Method:        method,
		Params:        json.RawMessage(params),
	}
	raw, err := protocol.EncodeFrame(f)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

func decodeSubprocessFrames(t *testing.T, stdout string) []*protocol.Frame {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(stdout))
	// Allow reading one oversized probe line (MaxFrameBytes+1) then skip via DecodeLine.
	sc.Buffer(make([]byte, 64*1024), protocol.MaxFrameBytes+4096)
	var frames []*protocol.Frame
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' || !json.Valid(line) {
			continue
		}
		f, err := protocol.DecodeLine(line)
		if err != nil {
			continue
		}
		frames = append(frames, f)
	}
	require.NoError(t, sc.Err())
	return frames
}
