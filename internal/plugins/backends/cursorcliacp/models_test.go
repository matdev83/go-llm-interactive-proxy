package cursorcliacp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

// acceptLoadedInventory loads from inv then commits via AcceptInventory, matching
// core's publish path (LoadModels alone does not update the Open allowlist).
func acceptLoadedInventory(t *testing.T, inv modelinventory.Provider) {
	t.Helper()
	if inv == nil {
		t.Fatal("nil inventory")
	}
	snap, err := inv.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a, ok := inv.(modelinventory.AcceptedInventory)
	if !ok {
		t.Fatalf("inventory type %T missing AcceptedInventory", inv)
	}
	a.AcceptInventory(snap.Models)
}

func TestCanonicalIDForNative_stripsLeadingCursorPrefix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		native, want string
	}{
		{name: "cursor-grok-4.5-high", native: "cursor-grok-4.5-high", want: "cursor/grok-4.5-high"},
		{name: "composer-2", native: "composer-2", want: "cursor/composer-2"},
		{name: "cursor-composer-2", native: "cursor-composer-2", want: "cursor/composer-2"},
		{name: "gpt-5.2", native: "gpt-5.2", want: "cursor/gpt-5.2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := canonicalIDForNative(tc.native)
			if got != tc.want {
				t.Fatalf("canonicalIDForNative(%q) = %q, want %q", tc.native, got, tc.want)
			}
		})
	}
}

func TestModelsFromListing_nativeAndCanonical(t *testing.T) {
	t.Parallel()

	models := modelsFromListing([]string{"cursor-grok-4.5-high", "composer-2"})
	if len(models) != 2 {
		t.Fatalf("len = %d", len(models))
	}
	if models[0].NativeID != "cursor-grok-4.5-high" || models[0].CanonicalID != "cursor/grok-4.5-high" {
		t.Fatalf("models[0] = %+v", models[0])
	}
	if models[1].NativeID != "composer-2" || models[1].CanonicalID != "cursor/composer-2" {
		t.Fatalf("models[1] = %+v", models[1])
	}
}

func TestListModelsArgs_order(t *testing.T) {
	t.Parallel()

	got := listModelsArgs("https://api.example/cursor")
	want := []string{"-e", "https://api.example/cursor", "--list-models"}
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v, want %v", got, want)
		}
	}
	got = listModelsArgs("  ")
	if len(got) != 1 || got[0] != "--list-models" {
		t.Fatalf("blank endpoint args = %v, want [--list-models]", got)
	}
	got = listModelsArgs("")
	if len(got) != 1 || got[0] != "--list-models" {
		t.Fatalf("empty endpoint args = %v, want [--list-models]", got)
	}
}

func TestModelsProvider_LoadModelsCommandArgsAndEndpoint(t *testing.T) {
	t.Parallel()

	var gotBinary string
	var gotEndpoint string
	p := modelsProvider{
		binary:   "agent",
		endpoint: "https://api.example/cursor",
		run: func(_ context.Context, binary, endpoint string) ([]byte, error) {
			gotBinary = binary
			gotEndpoint = endpoint
			return []byte("composer-2 - Composer 2\n"), nil
		},
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotBinary != "agent" || gotEndpoint != "https://api.example/cursor" {
		t.Fatalf("binary/endpoint = %q %q", gotBinary, gotEndpoint)
	}
	if snap.Source != modelinventory.SourceRemote {
		t.Fatalf("Source = %q", snap.Source)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "composer-2" {
		t.Fatalf("models = %+v", snap.Models)
	}
}

func TestModelsProvider_LoadModelsCommandArgsWithoutEndpoint(t *testing.T) {
	t.Parallel()

	var gotEndpoint string
	p := modelsProvider{
		binary: "agent",
		run: func(_ context.Context, _, endpoint string) ([]byte, error) {
			gotEndpoint = endpoint
			return []byte("gpt-5.2 - GPT\n"), nil
		},
	}
	_, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotEndpoint != "" {
		t.Fatalf("endpoint = %q, want empty", gotEndpoint)
	}
}

func TestModelsProvider_LoadModelsCommandFailureOperationalWithStderr(t *testing.T) {
	t.Parallel()

	p := modelsProvider{
		binary: "agent",
		run: func(context.Context, string, string) ([]byte, error) {
			return nil, errors.New("exit status 1: secret-token=abc stderr detail")
		},
	}
	_, err := p.LoadModels(context.Background())
	if !modelinventory.IsOperational(err) {
		t.Fatalf("IsOperational = false for %v", err)
	}
	disc := modelinventory.DiscoveryFromLoadError(err)
	if disc.ErrorCode != modelinventory.ErrorCodeUnavailable {
		t.Fatalf("ErrorCode = %q", disc.ErrorCode)
	}
	if strings.Contains(disc.ErrorCode, "secret") {
		t.Fatalf("ErrorCode leaked raw text")
	}
	var op *modelinventory.OperationalError
	if !errors.As(err, &op) || op.Err == nil {
		t.Fatalf("OperationalError.Err missing: %v", err)
	}
	if !strings.Contains(op.Err.Error(), "secret-token=abc") {
		t.Fatalf("stderr/detail must be in Err only: %v", op.Err)
	}
}

func TestDefaultModelsCommandRunner_sanitizesStderr(t *testing.T) {
	t.Parallel()

	// Exercise SanitizeBoundStderr through the same fold used by the runner.
	got := acp.SanitizeBoundStderr([]byte("fail\x00secret-token=abc\x7f"))
	if strings.ContainsRune(got, 0) || strings.ContainsRune(got, 127) {
		t.Fatalf("control runes remain: %q", got)
	}
	if !strings.Contains(got, "secret-token=abc") {
		t.Fatalf("printable stderr lost: %q", got)
	}
}

func TestModelsProvider_LoadModelsEmptyParseEmptySnapshot(t *testing.T) {
	t.Parallel()

	p := modelsProvider{
		binary: "agent",
		run: func(context.Context, string, string) ([]byte, error) {
			return []byte("Loading models...\nno separator here\n"), nil
		},
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("empty parse should not hard-fail: %v", err)
	}
	if len(snap.Models) != 0 {
		t.Fatalf("models = %+v, want empty (no defaultModels fallback)", snap.Models)
	}
}

func TestModelsProvider_LoadModelsTimeoutAndCancel(t *testing.T) {
	t.Parallel()

	t.Run("deadline", func(t *testing.T) {
		t.Parallel()
		p := modelsProvider{
			binary: "agent",
			run: func(ctx context.Context, _, _ string) ([]byte, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err := p.LoadModels(ctx)
		if !errors.Is(err, context.DeadlineExceeded) && !modelinventory.IsOperational(err) {
			t.Fatalf("err = %v", err)
		}
		disc := modelinventory.DiscoveryFromLoadError(err)
		if disc.ErrorCode != modelinventory.ErrorCodeTimeout && disc.ErrorCode != modelinventory.ErrorCodeUnavailable {
			t.Fatalf("ErrorCode = %q", disc.ErrorCode)
		}
	})

	t.Run("canceled", func(t *testing.T) {
		t.Parallel()
		p := modelsProvider{
			binary: "agent",
			run: func(ctx context.Context, _, _ string) ([]byte, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := p.LoadModels(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want canceled", err)
		}
	})
}

func TestDefaultModelsCommandRunner_noShell(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	missing := filepath.Join(t.TempDir(), "no-such-agent-binary")
	_, err := defaultModelsCommandRunner(ctx, missing, "")
	if err == nil {
		t.Fatal("expected direct exec failure for missing binary")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected context error: %v", err)
	}
}

func TestDefaultModelsCommandRunner_includesEndpointBeforeListModels(t *testing.T) {
	t.Parallel()

	// Portably prove arg order by wrapping the real runner with a stub binary is
	// OS-fragile (bat/cmd stderr). The mocked provider tests cover [-e, ep,
	// --list-models]; here we only assert the real runner still uses direct
	// exec.CommandContext when an endpoint is supplied (no shell, fails fast).
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	missing := filepath.Join(t.TempDir(), "no-such-agent-with-endpoint")
	_, err := defaultModelsCommandRunner(ctx, missing, "https://endpoint.example")
	if err == nil {
		t.Fatal("expected direct exec failure for missing binary with endpoint")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected context error: %v", err)
	}
}

func TestResolveNativeModel_seededIndex(t *testing.T) {
	t.Parallel()

	idx := acp.NewModelIndex(nil)
	idx.Replace([]modelinventory.Model{
		{CanonicalID: "cursor/grok-4.5-high", NativeID: "cursor-grok-4.5-high"},
		{CanonicalID: "cursor/composer-2", NativeID: "composer-2"},
	})
	cases := []struct {
		name                  string
		configured, effective string
		want                  string
	}{
		{name: "cursor-colon-native", configured: "", effective: "cursor:cursor-grok-4.5-high", want: "cursor-grok-4.5-high"},
		{name: "cursor-slash-canonical", configured: "", effective: "cursor/grok-4.5-high", want: "cursor-grok-4.5-high"},
		{name: "bare-native", configured: "", effective: "composer-2", want: "composer-2"},
		{name: "cursor-slash-composer", configured: "", effective: "cursor/composer-2", want: "composer-2"},
		{name: "configured-empty-effective", configured: "composer-2", effective: "", want: "composer-2"},
		{name: "configured-auto-effective", configured: "composer-2", effective: "auto", want: "composer-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := &cursorSpec{cfg: Config{ConnectorConfig: acp.ConnectorConfig{Model: tc.configured}}, index: idx}
			got, err := spec.resolveNativeModel(tc.effective)
			if err != nil {
				t.Fatalf("resolveNativeModel(%q,%q) error = %v", tc.configured, tc.effective, err)
			}
			if got != tc.want {
				t.Fatalf("resolveNativeModel(%q,%q) = %q, want %q", tc.configured, tc.effective, got, tc.want)
			}
		})
	}

	t.Run("unknown-canonical", func(t *testing.T) {
		t.Parallel()
		spec := &cursorSpec{cfg: Config{}, index: idx}
		_, err := spec.resolveNativeModel("cursor/not-a-real-model")
		if !errors.Is(err, ErrUnknownModel) {
			t.Fatalf("unknown error = %v, want ErrUnknownModel", err)
		}
	})
}

func TestBuildCommand_rejectsUnknownModel(t *testing.T) {
	t.Parallel()

	exe := filepath.Join(t.TempDir(), "agent")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := &cursorSpec{
		cfg:   Config{ConnectorConfig: acp.ConnectorConfig{Executable: exe}},
		index: acp.NewModelIndex(nil),
	}
	_, _, _, err := spec.BuildCommand("composer-2", "/ws")
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("BuildCommand error = %v, want ErrUnknownModel", err)
	}
}

func TestBuildCommand_nilSpec(t *testing.T) {
	t.Parallel()

	var spec *cursorSpec
	_, _, _, err := spec.BuildCommand("composer-2", "/ws")
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("nil BuildCommand error = %v, want ErrUnknownModel", err)
	}
}

func TestBuildCommand_rejectsEmptyModel(t *testing.T) {
	t.Parallel()

	exe := filepath.Join(t.TempDir(), "agent")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	idx := acp.NewModelIndex(nil)
	idx.Replace([]modelinventory.Model{{
		CanonicalID: "cursor/composer-2",
		NativeID:    "composer-2",
	}})
	spec := &cursorSpec{
		cfg:   Config{ConnectorConfig: acp.ConnectorConfig{Executable: exe}},
		index: idx,
	}
	_, _, _, err := spec.BuildCommand("", "/ws")
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("BuildCommand empty error = %v, want ErrUnknownModel", err)
	}
	_, _, _, err = spec.BuildCommand("   ", "/ws")
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("BuildCommand blank error = %v, want ErrUnknownModel", err)
	}
}

func TestResolveNativeModel_nilSafe(t *testing.T) {
	t.Parallel()

	var nilSpec *cursorSpec
	if _, err := nilSpec.resolveNativeModel("composer-2"); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("nil spec error = %v, want ErrUnknownModel", err)
	}
	spec := &cursorSpec{cfg: Config{}, index: nil}
	if _, err := spec.resolveNativeModel("composer-2"); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("nil index error = %v, want ErrUnknownModel", err)
	}
}

func TestBuildCommand_forwardsExactNativeModel(t *testing.T) {
	t.Parallel()

	exe := filepath.Join(t.TempDir(), "agent")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	idx := acp.NewModelIndex(nil)
	idx.Replace([]modelinventory.Model{
		{CanonicalID: "cursor/grok-4.5-high", NativeID: "cursor-grok-4.5-high"},
	})
	spec := &cursorSpec{
		cfg: Config{
			ConnectorConfig:   acp.ConnectorConfig{Executable: exe},
			CursorAPIEndpoint: "https://api.example",
			TrustWorkspace:    true,
		},
		index: idx,
		exe:   exe,
	}
	cmd, _, _, err := spec.BuildCommand("cursor-grok-4.5-high", "/ws")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd, "\x00")
	if !strings.Contains(joined, "\x00--model\x00cursor-grok-4.5-high") {
		t.Fatalf("command missing exact native --model: %v", cmd)
	}
	if !strings.Contains(joined, "\x00-e\x00https://api.example") {
		t.Fatalf("command missing -e endpoint: %v", cmd)
	}
}

type recordingStarter struct {
	mu   sync.Mutex
	n    int
	last []string
}

func (s *recordingStarter) Start(cmd []string, _ string, _ []string) (acp.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	s.last = append([]string(nil), cmd...)
	return nil, errors.New("recording starter: refuse start")
}

func (s *recordingStarter) starts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func (s *recordingStarter) lastCmd() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.last...)
}

func TestOpen_unknownCanonicalRejectsWithoutStartingProcess(t *testing.T) {
	t.Parallel()

	starter := &recordingStarter{}
	exe := filepath.Join(t.TempDir(), "agent")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	be := NewWithStarter(Config{
		ConnectorConfig: acp.ConnectorConfig{
			Executable:       exe,
			DefaultWorkspace: t.TempDir(),
		},
	}, starter)

	ws := t.TempDir()
	cwd, err := json.Marshal(ws)
	if err != nil {
		t.Fatal(err)
	}
	call := lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "cursorcliacp:cursor/not-a-real-model"},
		Extensions: map[string]json.RawMessage{
			"acp.cwd": cwd,
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}},
		}},
	}
	_, err = be.Open(context.Background(), call, routing.AttemptCandidate{})
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("Open error = %v, want ErrUnknownModel", err)
	}
	if starter.starts() != 0 {
		t.Fatalf("process starts = %d, want 0", starter.starts())
	}
}

func TestOpen_knownCanonicalForwardsExactNativeModel(t *testing.T) {
	t.Parallel()

	starter := &recordingStarter{}
	exe := filepath.Join(t.TempDir(), "agent")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	be := NewWithStarter(Config{
		ConnectorConfig: acp.ConnectorConfig{Executable: exe},
		Inventory: modelinventory.StaticProvider{
			Source: modelinventory.SourceStaticInline,
			Models: []modelinventory.Model{{
				CanonicalID: "cursor/grok-4.5-high",
				NativeID:    "cursor-grok-4.5-high",
				DisplayName: "cursor-grok-4.5-high",
			}},
		},
	}, starter)
	acceptLoadedInventory(t, be.ModelInventory)

	ws := t.TempDir()
	cwd, err := json.Marshal(ws)
	if err != nil {
		t.Fatal(err)
	}
	call := lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "cursorcliacp:cursor/grok-4.5-high"},
		Extensions: map[string]json.RawMessage{
			"acp.cwd": cwd,
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}},
		}},
	}
	_, err = be.Open(context.Background(), call, routing.AttemptCandidate{})
	if err == nil {
		t.Fatal("expected starter refusal error")
	}
	if starter.starts() != 1 {
		t.Fatalf("process starts = %d, want 1", starter.starts())
	}
	joined := strings.Join(starter.lastCmd(), "\x00")
	if !strings.Contains(joined, "\x00--model\x00cursor-grok-4.5-high") {
		t.Fatalf("Start cmd missing exact native --model: %v", starter.lastCmd())
	}
}

//nolint:paralleltest // mutates process env via t.Setenv
func TestNew_usesLiveOrUnavailableInventoryNotHardcodedDefaults(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	acp.ResetLookPathCache()
	missing := filepath.Join(t.TempDir(), "missing-agent-binary")
	t.Setenv("CURSOR_AGENT_BIN", missing)
	be, err := New(Config{ConnectorConfig: acp.ConnectorConfig{Executable: missing}})
	if err != nil {
		t.Fatal(err)
	}
	snap, loadErr := be.ModelInventory.LoadModels(context.Background())
	if loadErr != nil {
		if !modelinventory.IsOperational(loadErr) {
			t.Fatalf("expected operational failure, got %v", loadErr)
		}
		return
	}
	if len(snap.Models) != 0 {
		t.Fatalf("missing binary must yield empty snapshot or operational error, got %+v", snap.Models)
	}
}

func TestNewWithStarter_doesNotSpawnDiscovery(t *testing.T) {
	t.Parallel()

	be := NewWithStarter(Config{}, nil)
	if be.ModelInventory == nil {
		t.Fatal("ModelInventory nil")
	}
	_, err := be.ModelInventory.LoadModels(context.Background())
	if err == nil {
		t.Fatal("NewWithStarter must not advertise discovered/default models without injection")
	}
	if !modelinventory.IsOperational(err) {
		t.Fatalf("err = %v, want operational", err)
	}
}

func TestNewWithStarter_inventoryInjection(t *testing.T) {
	t.Parallel()

	be := NewWithStarter(Config{
		Inventory: modelinventory.StaticProvider{
			Source: modelinventory.SourceStaticInline,
			Models: []modelinventory.Model{{
				CanonicalID: "cursor/composer-2",
				NativeID:    "composer-2",
			}},
		},
	}, nil)
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "composer-2" {
		t.Fatalf("snap = %+v", snap)
	}
}

func TestOpen_refreshShrinkRejectsRemovedBeforeStart(t *testing.T) {
	t.Parallel()

	starter := &recordingStarter{}
	exe := filepath.Join(t.TempDir(), "agent")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	be := NewWithStarter(Config{
		ConnectorConfig: acp.ConnectorConfig{
			Executable:       exe,
			DefaultWorkspace: t.TempDir(),
		},
		Inventory: modelinventory.StaticProvider{
			Source: modelinventory.SourceStaticInline,
			Models: []modelinventory.Model{
				{CanonicalID: "cursor/composer-2", NativeID: "composer-2"},
				{CanonicalID: "cursor/gpt-5.2", NativeID: "gpt-5.2"},
			},
		},
	}, starter)
	acceptLoadedInventory(t, be.ModelInventory)

	ti, ok := be.ModelInventory.(*acp.TrackingInventory)
	if !ok {
		t.Fatalf("ModelInventory type = %T, want *acp.TrackingInventory", be.ModelInventory)
	}
	ti.SetInner(modelinventory.StaticProvider{
		Source: modelinventory.SourceStaticInline,
		Models: []modelinventory.Model{{
			CanonicalID: "cursor/gpt-5.2",
			NativeID:    "gpt-5.2",
		}},
	})
	acceptLoadedInventory(t, be.ModelInventory)

	ws := t.TempDir()
	cwd, err := json.Marshal(ws)
	if err != nil {
		t.Fatal(err)
	}
	call := lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "cursorcliacp:cursor/composer-2"},
		Extensions: map[string]json.RawMessage{
			"acp.cwd": cwd,
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}},
		}},
	}
	_, err = be.Open(context.Background(), call, routing.AttemptCandidate{})
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("Open removed model error = %v, want ErrUnknownModel", err)
	}
	if starter.starts() != 0 {
		t.Fatalf("process starts = %d, want 0", starter.starts())
	}
}

func TestOpen_staticOverrideConstrainsAfterLoadModels(t *testing.T) {
	t.Parallel()

	starter := &recordingStarter{}
	exe := filepath.Join(t.TempDir(), "agent")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	be := NewWithStarter(Config{
		ConnectorConfig: acp.ConnectorConfig{
			Executable:       exe,
			DefaultWorkspace: t.TempDir(),
		},
		Inventory: modelinventory.StaticProvider{
			Source: modelinventory.SourceRemote,
			Models: []modelinventory.Model{
				{CanonicalID: "cursor/composer-2", NativeID: "composer-2"},
				{CanonicalID: "cursor/grok-4.5-high", NativeID: "cursor-grok-4.5-high"},
			},
		},
	}, starter)
	acceptLoadedInventory(t, be.ModelInventory)

	ti, ok := be.ModelInventory.(*acp.TrackingInventory)
	if !ok {
		t.Fatalf("ModelInventory type = %T, want *acp.TrackingInventory", be.ModelInventory)
	}
	ti.SetInner(modelinventory.StaticProvider{
		Source: modelinventory.SourceStaticInline,
		Models: []modelinventory.Model{{
			CanonicalID: "cursor/composer-2",
			NativeID:    "composer-2",
		}},
	})
	acceptLoadedInventory(t, be.ModelInventory)

	ws := t.TempDir()
	cwd, err := json.Marshal(ws)
	if err != nil {
		t.Fatal(err)
	}
	call := lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "cursorcliacp:cursor/grok-4.5-high"},
		Extensions: map[string]json.RawMessage{
			"acp.cwd": cwd,
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}},
		}},
	}
	_, err = be.Open(context.Background(), call, routing.AttemptCandidate{})
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("Open overridden-away model error = %v, want ErrUnknownModel", err)
	}
	if starter.starts() != 0 {
		t.Fatalf("process starts = %d, want 0", starter.starts())
	}
}
