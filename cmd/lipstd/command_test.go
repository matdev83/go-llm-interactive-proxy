package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"path/filepath"
	"testing"
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
