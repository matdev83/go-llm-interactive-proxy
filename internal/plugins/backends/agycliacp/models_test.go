package agycliacp

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

func TestParseAGYModelsListing_knownPrettyNames(t *testing.T) {
	t.Parallel()

	stdout := strings.Join([]string{
		"Gemini 3.5 Flash (Medium)",
		"Gemini 3.5 Flash (High)",
		"Gemini 3.5 Flash (Low)",
		"Gemini 3.1 Pro (Low)",
		"Gemini 3.1 Pro (High)",
		"Claude Sonnet 4.6 (Thinking)",
		"Claude Opus 4.6 (Thinking)",
		"GPT-OSS 120B (Medium)",
	}, "\n") + "\n"

	models, warnings := parseAGYModelsListing(stdout)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	want := []struct {
		canonical, native string
	}{
		{"google/gemini-3.5-flash-medium", "Gemini 3.5 Flash (Medium)"},
		{"google/gemini-3.5-flash-high", "Gemini 3.5 Flash (High)"},
		{"google/gemini-3.5-flash-low", "Gemini 3.5 Flash (Low)"},
		{"google/gemini-3.1-pro-low", "Gemini 3.1 Pro (Low)"},
		{"google/gemini-3.1-pro-high", "Gemini 3.1 Pro (High)"},
		{"anthropic/claude-sonnet-4.6-thinking", "Claude Sonnet 4.6 (Thinking)"},
		{"anthropic/claude-opus-4.6-thinking", "Claude Opus 4.6 (Thinking)"},
		{"openai/gpt-oss-120b-medium", "GPT-OSS 120B (Medium)"},
	}
	if len(models) != len(want) {
		t.Fatalf("models len = %d, want %d: %+v", len(models), len(want), models)
	}
	for i, w := range want {
		if models[i].CanonicalID != w.canonical || models[i].NativeID != w.native {
			t.Fatalf("models[%d] = {%q,%q}, want {%q,%q}", i, models[i].CanonicalID, models[i].NativeID, w.canonical, w.native)
		}
		if models[i].DisplayName != w.native {
			t.Fatalf("DisplayName[%d] = %q, want %q", i, models[i].DisplayName, w.native)
		}
	}
}

func TestParseAGYModelsListing_blankDuplicateCRLFUnknown(t *testing.T) {
	t.Parallel()

	stdout := "Gemini 3.5 Flash (High)\r\n\r\nGemini 3.5 Flash (High)\r\nMystery Model (Ultra)\r\n  \nGemini 3.5 Flash (Low)\n"
	models, warnings := parseAGYModelsListing(stdout)
	if len(models) != 2 {
		t.Fatalf("models = %+v, want high+low", models)
	}
	if models[0].NativeID != "Gemini 3.5 Flash (High)" || models[1].NativeID != "Gemini 3.5 Flash (Low)" {
		t.Fatalf("models = %+v", models)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want 1 unknown", warnings)
	}
	if strings.Contains(strings.ToLower(warnings[0]), "slug") {
		t.Fatalf("warning must not invent slug: %q", warnings[0])
	}
	for _, w := range warnings {
		if strings.Contains(w, "token=") || strings.Contains(w, "secret") {
			t.Fatalf("warning leaked sensitive text: %q", w)
		}
	}
}

func TestNativePrettyForCanonical(t *testing.T) {
	t.Parallel()

	got, ok := nativePrettyForCanonical("google/gemini-3.5-flash-medium")
	if !ok || got != "Gemini 3.5 Flash (Medium)" {
		t.Fatalf("got %q %v", got, ok)
	}
	if _, ok := nativePrettyForCanonical("google/unknown-model"); ok {
		t.Fatal("unknown canonical must not map")
	}
}

func TestResolveNativeModel_mapsCanonicalAndRejectsUnknown(t *testing.T) {
	t.Parallel()

	idx := acp.NewModelIndex(nil)
	idx.Replace([]modelinventory.Model{
		{CanonicalID: "google/gemini-3.5-flash-high", NativeID: "Gemini 3.5 Flash (High)"},
		{CanonicalID: "google/gemini-3.5-flash-medium", NativeID: "Gemini 3.5 Flash (Medium)"},
		{CanonicalID: "google/gemini-3.1-pro-low", NativeID: "Gemini 3.1 Pro (Low)"},
		{CanonicalID: "anthropic/claude-sonnet-4.6-thinking", NativeID: "Claude Sonnet 4.6 (Thinking)"},
		{CanonicalID: "anthropic/claude-opus-4.6-thinking", NativeID: "Claude Opus 4.6 (Thinking)"},
	})
	cases := []struct {
		name                  string
		configured, effective string
		want                  string
	}{
		{name: "agy-colon-canonical", configured: "", effective: "agy:google/gemini-3.5-flash-high", want: "Gemini 3.5 Flash (High)"},
		{name: "agy-slash-canonical", configured: "", effective: "agy/anthropic/claude-sonnet-4.6-thinking", want: "Claude Sonnet 4.6 (Thinking)"},
		{name: "bare-canonical", configured: "", effective: "google/gemini-3.1-pro-low", want: "Gemini 3.1 Pro (Low)"},
		{name: "pretty-native", configured: "", effective: "Gemini 3.5 Flash (Medium)", want: "Gemini 3.5 Flash (Medium)"},
		{name: "configured-empty-effective", configured: "anthropic/claude-opus-4.6-thinking", effective: "", want: "Claude Opus 4.6 (Thinking)"},
		{name: "configured-auto-effective", configured: "anthropic/claude-opus-4.6-thinking", effective: "auto", want: "Claude Opus 4.6 (Thinking)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := &agySpec{cfg: Config{ConnectorConfig: acp.ConnectorConfig{Model: tc.configured}}, index: idx}
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
		spec := &agySpec{cfg: Config{}, index: idx}
		_, err := spec.resolveNativeModel("google/not-a-real-model")
		if !errors.Is(err, ErrUnknownModel) {
			t.Fatalf("unknown canonical error = %v, want ErrUnknownModel", err)
		}
	})
}

func TestResolveNativeModel_knownMappedButUnadvertisedRejected(t *testing.T) {
	t.Parallel()

	// Index advertises only Low; High remains in the conversion table but not allowlisted.
	idx := acp.NewModelIndex(nil)
	idx.Replace([]modelinventory.Model{
		{CanonicalID: "google/gemini-3.5-flash-low", NativeID: "Gemini 3.5 Flash (Low)"},
	})
	spec := &agySpec{cfg: Config{}, index: idx}

	_, err := spec.resolveNativeModel("google/gemini-3.5-flash-high")
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("unadvertised mapped model error = %v, want ErrUnknownModel", err)
	}
	got, err := spec.resolveNativeModel("google/gemini-3.5-flash-low")
	if err != nil || got != "Gemini 3.5 Flash (Low)" {
		t.Fatalf("advertised resolve = %q %v", got, err)
	}
}

func TestResolveNativeModel_nilSafe(t *testing.T) {
	t.Parallel()

	var nilSpec *agySpec
	if _, err := nilSpec.resolveNativeModel("google/gemini-3.5-flash-high"); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("nil spec error = %v, want ErrUnknownModel", err)
	}
	spec := &agySpec{cfg: Config{}, index: nil}
	if _, err := spec.resolveNativeModel("google/gemini-3.5-flash-high"); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("nil index error = %v, want ErrUnknownModel", err)
	}
}

func TestModelsProvider_LoadModelsSuccess(t *testing.T) {
	t.Parallel()

	p := modelsProvider{
		binary: "agy",
		run: func(context.Context, string) ([]byte, error) {
			return []byte("Gemini 3.5 Flash (High)\nUnknown X\n"), nil
		},
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceRemote {
		t.Fatalf("Source = %q", snap.Source)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "Gemini 3.5 Flash (High)" {
		t.Fatalf("models = %+v", snap.Models)
	}
	if len(snap.Warnings) != 1 {
		t.Fatalf("warnings = %v", snap.Warnings)
	}
}

func TestModelsProvider_LoadModelsCommandFailureOperational(t *testing.T) {
	t.Parallel()

	p := modelsProvider{
		binary: "agy",
		run: func(context.Context, string) ([]byte, error) {
			return nil, errors.New("exit status 1: secret-token=abc")
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
}

func TestDefaultModelsCommandRunner_nonzeroCapturesBoundedSanitizedStderr(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "agy-fail")
	script := ""
	if runtime.GOOS == "windows" {
		bin += ".bat"
		// Emit a long stderr with a control char and secret-looking token, then fail.
		script = "@echo off\r\necho secret-token=abc" + strings.Repeat("x", 9000) + " 1>&2\r\nexit /b 1\r\n"
	} else {
		script = "#!/bin/sh\nprintf 'secret-token=abc\\000" + strings.Repeat("x", 9000) + "' >&2\nexit 1\n"
	}
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := defaultModelsCommandRunner(ctx, bin)
	if err == nil {
		t.Fatal("expected nonzero exit error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "secret-token=abc") {
		t.Fatalf("stderr detail missing from wrapped error: %v", err)
	}
	if strings.Contains(msg, "\x00") {
		t.Fatalf("stderr must strip null bytes: %q", msg)
	}
	if len(msg) > acp.MaxBoundStderrBytes+256 {
		t.Fatalf("stderr not bounded: len=%d", len(msg))
	}

	p := modelsProvider{binary: bin}
	_, loadErr := p.LoadModels(context.Background())
	if !modelinventory.IsOperational(loadErr) {
		t.Fatalf("IsOperational = false for %v", loadErr)
	}
	disc := modelinventory.DiscoveryFromLoadError(loadErr)
	if disc.ErrorCode != modelinventory.ErrorCodeUnavailable {
		t.Fatalf("ErrorCode = %q, want stable unavailable", disc.ErrorCode)
	}
	if strings.Contains(disc.ErrorCode, "secret") {
		t.Fatalf("ErrorCode leaked raw text: %q", disc.ErrorCode)
	}
}

func TestSanitizeBoundStderr_clonesBeforeTruncate(t *testing.T) {
	t.Parallel()

	orig := []byte(strings.Repeat("a", acp.MaxBoundStderrBytes+64))
	marker := byte('Z')
	orig[0] = marker
	_ = sanitizeBoundStderr(orig)
	if orig[0] != marker {
		t.Fatal("sanitizeBoundStderr must not mutate caller's byte 0")
	}
	orig[acp.MaxBoundStderrBytes] = 'X'
	again := sanitizeBoundStderr(orig)
	if len(again) != acp.MaxBoundStderrBytes {
		t.Fatalf("sanitized len = %d, want %d", len(again), acp.MaxBoundStderrBytes)
	}
	if again[0] != marker {
		t.Fatalf("sanitized[0] = %q, want %q", again[0], marker)
	}
}

func TestModelsProvider_LoadModelsTimeoutAndCancel(t *testing.T) {
	t.Parallel()

	t.Run("deadline", func(t *testing.T) {
		t.Parallel()
		p := modelsProvider{
			binary: "agy",
			run: func(ctx context.Context, _ string) ([]byte, error) {
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
			binary: "agy",
			run: func(ctx context.Context, _ string) ([]byte, error) {
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

func TestResolveAGYBinary_configuredAndEnv(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "agy-bin")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := resolveAGYBinary(bin)
	if err != nil || got != bin {
		t.Fatalf("configured resolve = %q %v", got, err)
	}

	t.Setenv("AGY_BINARY", bin)
	got, err = resolveAGYBinary("")
	if err != nil || got != bin {
		t.Fatalf("env resolve = %q %v", got, err)
	}
}

func TestResolveAGYBinary_pathLookup(t *testing.T) {
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)
	dir := t.TempDir()
	name := "agy"
	if runtime.GOOS == "windows" {
		name = "agy.exe"
	}
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGY_BINARY", "")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := resolveAGYBinary("")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != name && !strings.HasSuffix(got, name) {
		t.Fatalf("PATH resolve = %q, want basename %s", got, name)
	}
}

func TestBuildCommand_forwardsExactPrettyModel(t *testing.T) {
	t.Parallel()

	wrapper := filepath.Join(t.TempDir(), "wrapper")
	if runtime.GOOS == "windows" {
		wrapper += ".exe"
	}
	if err := os.WriteFile(wrapper, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	idx := acp.NewModelIndex(nil)
	idx.Replace([]modelinventory.Model{{
		CanonicalID: "google/gemini-3.5-flash-medium",
		NativeID:    "Gemini 3.5 Flash (Medium)",
	}})
	spec := &agySpec{
		cfg: Config{
			WrapperExecutable: wrapper,
			AGYBinary:         "/usr/bin/agy",
			SkipPermissions:   true,
		},
		index: idx,
		exe:   wrapper,
	}
	native, err := spec.resolveNativeModel("google/gemini-3.5-flash-medium")
	if err != nil {
		t.Fatal(err)
	}
	cmd, _, _, err := spec.BuildCommand(native, "/ws")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd, "\x00")
	if !strings.Contains(joined, "\x00--model\x00Gemini 3.5 Flash (Medium)") {
		t.Fatalf("command missing exact pretty --model: %v", cmd)
	}
}

func TestBuildCommand_rejectsUnknownModel(t *testing.T) {
	t.Parallel()

	wrapper := filepath.Join(t.TempDir(), "wrapper")
	if runtime.GOOS == "windows" {
		wrapper += ".exe"
	}
	if err := os.WriteFile(wrapper, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := &agySpec{cfg: Config{WrapperExecutable: wrapper}, index: acp.NewModelIndex(nil)}
	_, _, _, err := spec.BuildCommand("google/not-a-real-model", "/ws")
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("BuildCommand error = %v, want ErrUnknownModel", err)
	}
}

func TestBuildCommand_nilSpec(t *testing.T) {
	t.Parallel()

	var spec *agySpec
	_, _, _, err := spec.BuildCommand("Gemini 3.5 Flash (High)", "/ws")
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("nil BuildCommand error = %v, want ErrUnknownModel", err)
	}
}

func TestBuildCommand_rejectsKnownButUnadvertisedPretty(t *testing.T) {
	t.Parallel()

	wrapper := filepath.Join(t.TempDir(), "wrapper")
	if runtime.GOOS == "windows" {
		wrapper += ".exe"
	}
	if err := os.WriteFile(wrapper, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	idx := acp.NewModelIndex(nil)
	idx.Replace([]modelinventory.Model{{
		CanonicalID: "google/gemini-3.5-flash-low",
		NativeID:    "Gemini 3.5 Flash (Low)",
	}})
	spec := &agySpec{cfg: Config{WrapperExecutable: wrapper}, index: idx}
	_, _, _, err := spec.BuildCommand("Gemini 3.5 Flash (High)", "/ws")
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("unadvertised pretty BuildCommand error = %v, want ErrUnknownModel", err)
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
	wrapper := filepath.Join(t.TempDir(), "wrapper")
	if runtime.GOOS == "windows" {
		wrapper += ".exe"
	}
	if err := os.WriteFile(wrapper, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	be := NewWithStarter(Config{
		WrapperExecutable: wrapper,
		ConnectorConfig:   acp.ConnectorConfig{DefaultWorkspace: t.TempDir()},
	}, starter)

	ws := t.TempDir()
	cwd, err := json.Marshal(ws)
	if err != nil {
		t.Fatal(err)
	}
	call := lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "agycliacp:google/not-a-real-model"},
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

func TestOpen_knownButUnadvertisedRejectsWithoutStartingProcess(t *testing.T) {
	t.Parallel()

	starter := &recordingStarter{}
	wrapper := filepath.Join(t.TempDir(), "wrapper")
	if runtime.GOOS == "windows" {
		wrapper += ".exe"
	}
	if err := os.WriteFile(wrapper, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	be := NewWithStarter(Config{
		WrapperExecutable: wrapper,
		ConnectorConfig:   acp.ConnectorConfig{DefaultWorkspace: t.TempDir()},
		Inventory: modelinventory.StaticProvider{
			Source: modelinventory.SourceStaticInline,
			Models: []modelinventory.Model{{
				CanonicalID: "google/gemini-3.5-flash-low",
				NativeID:    "Gemini 3.5 Flash (Low)",
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
		Route: lipapi.RouteIntent{Selector: "agycliacp:google/gemini-3.5-flash-high"},
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
		t.Fatalf("Open unadvertised error = %v, want ErrUnknownModel", err)
	}
	if starter.starts() != 0 {
		t.Fatalf("process starts = %d, want 0", starter.starts())
	}
}

func TestOpen_knownCanonicalForwardsExactPrettyModel(t *testing.T) {
	t.Parallel()

	starter := &recordingStarter{}
	wrapper := filepath.Join(t.TempDir(), "wrapper")
	if runtime.GOOS == "windows" {
		wrapper += ".exe"
	}
	if err := os.WriteFile(wrapper, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	be := NewWithStarter(Config{
		WrapperExecutable: wrapper,
		SkipPermissions:   true,
		Inventory: modelinventory.StaticProvider{
			Source: modelinventory.SourceStaticInline,
			Models: []modelinventory.Model{{
				CanonicalID: "google/gemini-3.5-flash-medium",
				NativeID:    "Gemini 3.5 Flash (Medium)",
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
		Route: lipapi.RouteIntent{Selector: "agycliacp:google/gemini-3.5-flash-medium"},
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
	if !strings.Contains(joined, "\x00--model\x00Gemini 3.5 Flash (Medium)") {
		t.Fatalf("Start cmd missing exact pretty --model: %v", starter.lastCmd())
	}
}

//nolint:paralleltest // mutates process env via t.Setenv
func TestNew_usesLiveOrUnavailableInventoryNotHardcodedDefaults(t *testing.T) {
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)
	t.Setenv("PATH", t.TempDir())
	missing := filepath.Join(t.TempDir(), "missing-agy-binary")
	t.Setenv("AGY_BINARY", missing)
	be, err := New(Config{
		WrapperExecutable: filepath.Join(t.TempDir(), "missing-wrapper"),
		AGYBinary:         missing,
	})
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
				CanonicalID: "google/gemini-3.5-flash-high",
				NativeID:    "Gemini 3.5 Flash (High)",
			}},
		},
	}, nil)
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "Gemini 3.5 Flash (High)" {
		t.Fatalf("snap = %+v", snap)
	}
}

func TestDefaultModelsCommandRunner_noShell(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	missing := filepath.Join(t.TempDir(), "no-such-agy-binary")
	_, err := defaultModelsCommandRunner(ctx, missing)
	if err == nil {
		t.Fatal("expected direct exec failure for missing binary")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected context error: %v", err)
	}
}
