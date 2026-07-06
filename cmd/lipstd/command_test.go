package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestParseCommandName_defaultServe(t *testing.T) {
	t.Parallel()
	n, err := parseCommandName(nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != CommandServe {
		t.Fatalf("got %q", n)
	}
	n2, err := parseCommandName([]string{})
	if err != nil || n2 != CommandServe {
		t.Fatalf("empty args: %v %q", err, n2)
	}
}

func TestParseCommandName_explicitSubcommands(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"serve", "check-config", "routes", "inventory"} {
		t.Run(s, func(t *testing.T) {
			t.Parallel()
			n, err := parseCommandName([]string{s})
			if err != nil {
				t.Fatal(err)
			}
			if string(n) != s {
				t.Fatalf("got %q", n)
			}
		})
	}
}

func TestParseCommandName_unknown(t *testing.T) {
	t.Parallel()
	_, err := parseCommandName([]string{"nope"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseCLIPrefix_configValueMayEqualSubcommandName(t *testing.T) {
	t.Parallel()
	prefix, n, tail := parseCLIPrefix([]string{"--config", "routes", "check-config"})
	wantPrefix := []string{"--config", "routes"}
	if len(prefix) != len(wantPrefix) || prefix[0] != wantPrefix[0] || prefix[1] != wantPrefix[1] {
		t.Fatalf("prefix %#v", prefix)
	}
	if n != CommandCheckConfig || len(tail) != 0 {
		t.Fatalf("cmd=%q tail=%v", n, tail)
	}
	prefix2, n2, tail2 := parseCLIPrefix([]string{"--config", "inventory", "inventory"})
	if len(prefix2) != 2 || prefix2[0] != "--config" || prefix2[1] != "inventory" {
		t.Fatalf("prefix %#v", prefix2)
	}
	if n2 != CommandInventory || len(tail2) != 0 {
		t.Fatalf("cmd=%q tail=%v", n2, tail2)
	}
}

func TestParseArgs_configPathEqualsSubcommandName(t *testing.T) {
	t.Parallel()
	var usage bytes.Buffer
	cfg, name, err := ParseArgs([]string{"--config", "routes", "check-config"}, &usage)
	if err != nil {
		t.Fatal(err)
	}
	if name != CommandCheckConfig || cfg != "routes" {
		t.Fatalf("cfg=%q name=%q", cfg, name)
	}
	var usage2 bytes.Buffer
	cfg2, name2, err2 := ParseArgs([]string{"--config", "inventory", "inventory"}, &usage2)
	if err2 != nil {
		t.Fatal(err2)
	}
	if name2 != CommandInventory || cfg2 != "inventory" {
		t.Fatalf("cfg=%q name=%q", cfg2, name2)
	}
}

func TestParseArgs_routesOnlyUsesDefaultConfigPath(t *testing.T) {
	t.Parallel()
	var usage bytes.Buffer
	cfg, name, err := ParseArgs([]string{"routes"}, &usage)
	if err != nil {
		t.Fatal(err)
	}
	if name != CommandRoutes {
		t.Fatalf("name=%q", name)
	}
	if cfg != "./config/config.yaml" {
		t.Fatalf("cfg=%q want default", cfg)
	}
}

func TestParseCLIPrefix_subcommandPositions(t *testing.T) {
	t.Parallel()
	prefix, n, tail := parseCLIPrefix([]string{"--config", "a.yaml", "routes"})
	wantPrefix := []string{"--config", "a.yaml"}
	if len(prefix) != len(wantPrefix) || prefix[0] != wantPrefix[0] || prefix[1] != wantPrefix[1] {
		t.Fatalf("prefix %#v", prefix)
	}
	if n != CommandRoutes || len(tail) != 0 {
		t.Fatalf("cmd=%q tail=%v", n, tail)
	}
	prefix2, n2, tail2 := parseCLIPrefix([]string{"serve", "--config", "b.yaml"})
	if len(prefix2) != 0 || n2 != CommandServe {
		t.Fatalf("got prefix=%v cmd=%q", prefix2, n2)
	}
	if len(tail2) != 2 || tail2[0] != "--config" || tail2[1] != "b.yaml" {
		t.Fatalf("tail %#v", tail2)
	}
}

func TestParseArgs_configAfterSubcommand(t *testing.T) {
	t.Parallel()
	var usage bytes.Buffer
	cfg, name, err := ParseArgs([]string{"serve", "--config", filepath.Join("..", "..", "config", "config.yaml")}, &usage)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join("..", "..", "config", "config.yaml")
	if name != CommandServe || filepath.Clean(cfg) != filepath.Clean(wantPath) {
		t.Fatalf("cfg=%q name=%q", cfg, name)
	}
}

func TestParseArgs_configBeforeSubcommand(t *testing.T) {
	t.Parallel()
	var usage bytes.Buffer
	path := filepath.Join("..", "..", "config", "config.yaml")
	cfg, name, err := ParseArgs([]string{"--config", path, "routes"}, &usage)
	if err != nil {
		t.Fatal(err)
	}
	if name != CommandRoutes || cfg != path {
		t.Fatalf("cfg=%q name=%q", cfg, name)
	}
}

func TestParseArgs_lastConfigWins(t *testing.T) {
	t.Parallel()
	var usage bytes.Buffer
	cfg, _, err := ParseArgs([]string{"--config", "first.yaml", "serve", "--config", "second.yaml"}, &usage)
	if err != nil {
		t.Fatal(err)
	}
	if cfg != "second.yaml" {
		t.Fatalf("got %q", cfg)
	}
}

func TestParseArgs_help(t *testing.T) {
	t.Parallel()
	var usage bytes.Buffer
	_, _, err := ParseArgs([]string{"-h"}, &usage)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("got %v", err)
	}
	if usage.Len() == 0 {
		t.Fatal("expected usage text")
	}
}

func TestRunCommand_checkConfig_reference(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := filepath.Join("..", "..", "config", "config.yaml")
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandCheckConfig,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr: %s", errb.String())
	}
	if out.String() == "" {
		t.Fatal("expected stdout message")
	}
}

func TestRunCommand_routes_emitsJSON(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := filepath.Join("..", "..", "config", "config.yaml")
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandRoutes,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte(`"effective_default_route"`)) {
		t.Fatalf("stdout: %s", out.String())
	}
}

func TestRunCommand_inventory_emitsJSON(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := filepath.Join("..", "..", "config", "config.yaml")
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandInventory,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr: %s", errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte(`"frontends"`)) {
		t.Fatalf("stdout: %s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte(`"extensions"`)) {
		t.Fatalf("stdout: %s", out.String())
	}
}

func TestRunCommand_checkConfig_dogfoodLocalStubExample(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml")
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandCheckConfig,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr: %s", errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("configuration is valid")) {
		t.Fatalf("stdout: %s", out.String())
	}
}

func TestRunCommand_checkConfig_multiInstanceExample(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := filepath.Join("..", "..", "config", "config.multi-instance.example.yaml")
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandCheckConfig,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr: %s", errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("configuration is valid")) {
		t.Fatalf("stdout: %s", out.String())
	}
}

func TestRunCommand_routes_dogfoodLocalStubExample(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml")
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandRoutes,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte(`"effective_default_route"`)) {
		t.Fatalf("stdout: %s", out.String())
	}
	var routes struct {
		EffectiveDefaultRoute string `json:"effective_default_route"`
		CredentialPosture     string `json:"credential_posture"`
	}
	if err := json.Unmarshal(out.Bytes(), &routes); err != nil {
		t.Fatal(err)
	}
	if routes.EffectiveDefaultRoute != "dogfood-local:stub-default" {
		t.Fatalf("effective_default_route=%q", routes.EffectiveDefaultRoute)
	}
	if routes.CredentialPosture != "all_local_stub" {
		t.Fatalf("credential_posture=%q", routes.CredentialPosture)
	}
}

func TestRunCommand_inventory_dogfoodLocalStubExample(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml")
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandInventory,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte(`"dogfood-local"`)) {
		t.Fatalf("expected local stub backend id in inventory stdout: %s", out.String())
	}
	var inv struct {
		Frontends  json.RawMessage `json:"frontends"`
		Extensions json.RawMessage `json:"extensions"`
	}
	if err := json.Unmarshal(out.Bytes(), &inv); err != nil {
		t.Fatal(err)
	}
	if len(inv.Frontends) == 0 || len(inv.Extensions) == 0 {
		t.Fatalf("unexpected inventory shape: %s", out.String())
	}
}

func TestRunCommand_serve_bootstrapFailsOnMissingConfig(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	missing := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandServe,
		ConfigPath: missing,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 1 {
		t.Fatalf("exit %d want 1 stderr=%s stdout=%s", code, errb.String(), out.String())
	}
	if !bytes.Contains(errb.Bytes(), []byte("bootstrap failed:")) {
		t.Fatalf("stderr: %q", errb.String())
	}
}

func TestRunCommand_unknownName(t *testing.T) {
	t.Parallel()
	var errb bytes.Buffer
	code := RunCommand(context.Background(), CommandOptions{Name: "weird", ErrorOut: &errb})
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestRunCommand_nilContext(t *testing.T) {
	t.Parallel()
	var errb bytes.Buffer
	code := RunCommand(nil, CommandOptions{Name: CommandServe, ErrorOut: &errb}) //nolint:staticcheck // intentional nil ctx contract
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !bytes.Contains(errb.Bytes(), []byte("nil context")) {
		t.Fatalf("stderr: %q", errb.String())
	}
}

func TestParseArgs_autoResumeFlagMayDisable(t *testing.T) {
	t.Parallel()
	var usage bytes.Buffer
	opts, err := ParseArgsFull([]string{"--auto-resume=true", "serve", "--auto-resume=false"}, &usage)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Name != CommandServe {
		t.Fatalf("name=%q", opts.Name)
	}
	if opts.StreamRecovery.CLIEnabled == nil || *opts.StreamRecovery.CLIEnabled {
		t.Fatalf("expected trailing CLI false override, got %#v", opts.StreamRecovery.CLIEnabled)
	}
}

func TestParseArgs_autoResumeDurations(t *testing.T) {
	t.Parallel()
	var usage bytes.Buffer
	opts, err := ParseArgsFull([]string{"serve", "--auto-resume-idle-timeout=20s", "--auto-resume-grace-period=2s"}, &usage)
	if err != nil {
		t.Fatal(err)
	}
	if opts.StreamRecovery.CLIIdleTimeout.String() != "20s" {
		t.Fatalf("idle timeout=%s", opts.StreamRecovery.CLIIdleTimeout)
	}
	if opts.StreamRecovery.CLIGracePeriod.String() != "2s" {
		t.Fatalf("grace period=%s", opts.StreamRecovery.CLIGracePeriod)
	}
}

func TestParseArgs_multiUserFlag(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		want *bool
	}{
		{name: "not_set", args: []string{"serve"}, want: nil},
		{name: "prefix", args: []string{"--multi-user", "serve"}, want: new(true)},
		{name: "tail", args: []string{"serve", "--multi-user"}, want: new(true)},
		{name: "equals_true", args: []string{"serve", "--multi-user=true"}, want: new(true)},
		{name: "explicit_false", args: []string{"serve", "--multi-user=false"}, want: new(false)},
		{name: "prefix_then_tail_overrides", args: []string{"--multi-user=true", "serve", "--multi-user=false"}, want: new(false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var usage bytes.Buffer
			opts, err := ParseArgsFull(tc.args, &usage)
			if err != nil {
				t.Fatal(err)
			}
			if opts.Name != CommandServe {
				t.Fatalf("name=%q", opts.Name)
			}
			got := opts.MultiUser
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("want nil, got %v", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("want %v, got nil", *tc.want)
			case tc.want != nil && *tc.want != *got:
				t.Fatalf("want %v, got %v", *tc.want, *got)
			}
		})
	}
}

//go:fix inline
func boolPtr(b bool) *bool { return new(b) }

func TestRunCommand_serve_multiUserFlagInconsistentWithSingleUserConfig(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml")
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandServe,
		ConfigPath: cfgPath,
		MultiUser:  new(true),
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 2 {
		t.Fatalf("exit %d want 2 stderr=%s stdout=%s", code, errb.String(), out.String())
	}
	if !bytes.Contains(errb.Bytes(), []byte("--multi-user")) {
		t.Fatalf("stderr should mention --multi-user: %q", errb.String())
	}
}

func TestRunCommand_serve_multiUserConfigRequiresFlag(t *testing.T) {
	t.Parallel()
	cfgPath := writeMultiUserTempConfig(t)
	var out, errb bytes.Buffer
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandServe,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 2 {
		t.Fatalf("exit %d want 2 stderr=%s stdout=%s", code, errb.String(), out.String())
	}
	if !bytes.Contains(errb.Bytes(), []byte("--multi-user")) {
		t.Fatalf("stderr should mention --multi-user: %q", errb.String())
	}
}

func TestValidateServeMultiUserGate_multiUserConfigWithFlagPasses(t *testing.T) {
	t.Parallel()
	cfgPath := writeMultiUserTempConfig(t)
	if err := validateServeMultiUserGate(cfgPath, new(true)); err != nil {
		t.Fatalf("expected gate to pass with --multi-user=true on multi_user config: %v", err)
	}
}

func TestValidateServeMultiUserGate_multiUserConfigRequiresFlag(t *testing.T) {
	t.Parallel()
	cfgPath := writeMultiUserTempConfig(t)
	if err := validateServeMultiUserGate(cfgPath, nil); !errors.Is(err, accessmode.ErrMultiUserFlagRequired) {
		t.Fatalf("want ErrMultiUserFlagRequired, got %v", err)
	}
}

func TestValidateServeMultiUserGate_multiUserConfigFlagFalseRejected(t *testing.T) {
	t.Parallel()
	cfgPath := writeMultiUserTempConfig(t)
	if err := validateServeMultiUserGate(cfgPath, new(false)); !errors.Is(err, accessmode.ErrMultiUserFlagRequired) {
		t.Fatalf("explicit --multi-user=false must not satisfy multi_user: got %v", err)
	}
}

func TestValidateServeMultiUserGate_singleUserConfigFlagTrueRejected(t *testing.T) {
	t.Parallel()
	cfgPath := filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml")
	if err := validateServeMultiUserGate(cfgPath, new(true)); !errors.Is(err, accessmode.ErrMultiUserFlagInconsistent) {
		t.Fatalf("want ErrMultiUserFlagInconsistent, got %v", err)
	}
}

// TestBuildBootstrap_multiUserConfigLocalStubPassesPosture confirms the gate
// removal did not disable runtimebundle posture/security validation: a
// multi_user config whose only enabled backend is local-stub (BackendAccessAny,
// CredentialNone) still assembles successfully.
func TestBuildBootstrap_multiUserConfigLocalStubPassesPosture(t *testing.T) {
	t.Parallel()
	cfgPath := writeMultiUserTempConfig(t)
	res, err := runtimebundle.BuildBootstrap(context.Background(), runtimebundle.BuildBootstrapInput{
		ConfigPath: cfgPath,
		Mode:       runtimebundle.BootstrapServe,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}
	if res.ShutdownTracing != nil {
		_ = res.ShutdownTracing(context.Background())
	}
}

func writeMultiUserTempConfig(t *testing.T) string {
	t.Helper()
	const cfg = `server:
  address: "127.0.0.1:18080"
  auth_mode: external
access:
  mode: multi_user
auth:
  handler: local_api_key
  required_level: api_key
  local_api_keys:
    - key_id: k1
      principal_id: p1
      key: "test-key-at-least-16-chars"
routing:
  max_attempts: 3
  default_route: "dogfood-local:stub-default"
continuity:
  in_memory: true
  store: memory
logging:
  level: info
  format: text
diagnostics:
  enabled: false
hooks:
  tool_reactor_error_policy: fail_open
plugins:
  frontends:
    - id: openai-responses
      enabled: true
      config: {}
    - id: openai-legacy
      enabled: true
      config: {}
    - id: anthropic
      enabled: true
      config: {}
    - id: gemini
      enabled: true
      config: {}
  backends:
    - id: openai-responses
      enabled: false
      config: {}
    - id: openai-legacy
      enabled: false
      config: {}
    - id: anthropic
      enabled: false
      config: {}
    - id: gemini
      enabled: false
      config: {}
    - id: bedrock
      enabled: false
      config: {}
    - id: acp
      enabled: false
      config: {}
    - id: openrouter
      enabled: false
      config: {}
    - id: nvidia
      enabled: false
      config: {}
    - id: opencode-go
      enabled: false
      config: {}
    - id: opencode-zen
      enabled: false
      config: {}
    - id: ollama
      enabled: false
      config: {}
    - id: ollama-cloud
      enabled: false
      config: {}
    - id: llamacpp
      enabled: false
      config: {}
    - id: lmstudio
      enabled: false
      config: {}
    - id: vllm
      enabled: false
      config: {}
    - kind: local-stub
      id: dogfood-local
      enabled: true
      config:
        text: "[dogfood] local stub"
        input_tokens: 3
        output_tokens: 7
  features:
    - id: submit-noop
      enabled: true
      config: {}
    - id: parts-noop
      enabled: true
      config: {}
    - id: tool-reactor-noop
      enabled: true
      config: {}
`
	path := filepath.Join(t.TempDir(), "multi-user.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
