package product

import (
	"strconv"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

func TestBuildCommandForwardsFourHourTimeoutByDefault(t *testing.T) {
	spec := testAgySpec(t, Config{})
	cmd, _, _, err := spec.BuildCommand("google/gemini-3.5-flash-high", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assertFlagValue(t, cmd, "--timeout-seconds", strconv.Itoa(DefaultTimeoutSeconds))
}

func TestBuildCommandForwardsExplicitTimeout(t *testing.T) {
	spec := testAgySpec(t, Config{TimeoutSeconds: 45})
	cmd, _, _, err := spec.BuildCommand("google/gemini-3.5-flash-high", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assertFlagValue(t, cmd, "--timeout-seconds", "45")
}

func TestResolveNativeModel_DynamicModels(t *testing.T) {
	index := acp.NewModelIndex(agyCanonicalFallback)
	index.Replace([]modelinventory.Model{
		{
			CanonicalID: "google/gemini-3.7-flash",
			NativeID:    "gemini-3.7-flash-high",
			DisplayName: "Gemini 3.7 Flash (High)",
		},
	})
	spec := &agySpec{
		cfg:   Config{},
		exe:   "go-agy-acp-wrapper",
		index: index,
	}

	got, err := spec.resolveNativeModel("google/gemini-3.7-flash")
	if err != nil || got != "gemini-3.7-flash-high" {
		t.Fatalf("resolveNativeModel(google/gemini-3.7-flash) = %q, %v", got, err)
	}

	got, err = spec.resolveNativeModel("gemini-3.7-flash-high")
	if err != nil || got != "gemini-3.7-flash-high" {
		t.Fatalf("resolveNativeModel(gemini-3.7-flash-high) = %q, %v", got, err)
	}
}

func testAgySpec(t *testing.T, cfg Config) *agySpec {

	t.Helper()
	index := acp.NewModelIndex(agyCanonicalFallback)
	index.Replace([]modelinventory.Model{{
		CanonicalID: "google/gemini-3.5-flash-high",
		NativeID:    "google/gemini-3.5-flash-high",
		DisplayName: "Gemini 3.5 Flash (High)",
	}})
	return &agySpec{
		cfg:   cfg,
		exe:   "go-agy-acp-wrapper",
		index: index,
	}
}

func assertFlagValue(t *testing.T, cmd []string, flag, want string) {
	t.Helper()
	for i, arg := range cmd {
		if arg == flag {
			if i+1 >= len(cmd) {
				t.Fatalf("flag %s missing value in %v", flag, cmd)
			}
			if cmd[i+1] != want {
				t.Fatalf("flag %s = %q, want %q in %v", flag, cmd[i+1], want, cmd)
			}
			return
		}
	}
	t.Fatalf("flag %s not found in %v", flag, cmd)
}
