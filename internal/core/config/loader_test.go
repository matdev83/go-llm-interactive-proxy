package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
)

func TestLoadFile_effectiveAccessMode_defaultsToSingleUser(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	mode, err := cfg.EffectiveAccessMode()
	if err != nil {
		t.Fatal(err)
	}
	if want := "single_user"; string(mode) != want {
		t.Fatalf("EffectiveAccessMode: want %q got %q", want, mode)
	}
}

func TestLoadFile_externalWildcardBindFailsWithoutMultiUserAccess(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
server:
  address: ":8080"
  auth_mode: external
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !errors.Is(err, accessmode.ErrSingleUserBroadBind) {
		t.Fatalf("want %v, got %v", accessmode.ErrSingleUserBroadBind, err)
	}
}

func TestLoadFile_migration_broadBindIPv4AllInterfacesRejectedInSingleUser(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
server:
  address: "0.0.0.0:8080"
  auth_mode: external
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !errors.Is(err, accessmode.ErrSingleUserBroadBind) {
		t.Fatalf("want %v, got %v", accessmode.ErrSingleUserBroadBind, err)
	}
}

func TestLoadFile_migration_broadBindIPv6AllInterfacesRejectedInSingleUser(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
server:
  address: "[::]:9000"
  auth_mode: external
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !errors.Is(err, accessmode.ErrSingleUserBroadBind) {
		t.Fatalf("want %v, got %v", accessmode.ErrSingleUserBroadBind, err)
	}
}

func TestLoadFile_migration_broadBindBarePortRejectedInSingleUser(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
server:
  address: ":9090"
  auth_mode: external
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !errors.Is(err, accessmode.ErrSingleUserBroadBind) {
		t.Fatalf("want %v, got %v", accessmode.ErrSingleUserBroadBind, err)
	}
}

func TestLoadFile_migration_multiUserLocalAPIKeyAndBroadBind_loads(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
access:
  mode: multi_user
server:
  address: "0.0.0.0:8080"
  auth_mode: external
auth:
  handler: local_api_key
  required_level: api_key
  local_api_keys:
    - key_id: k1
      principal_id: u1
      key: "loadfile-migration-test-key"
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	mode, err := cfg.EffectiveAccessMode()
	if err != nil || string(mode) != "multi_user" {
		t.Fatalf("mode: %v err=%v", mode, err)
	}
	if cfg.Server.Address != "0.0.0.0:8080" {
		t.Fatalf("address: %q", cfg.Server.Address)
	}
}

func TestLoadFile_omittedServerAddress_defaultsToExplicitLoopback(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if want := "127.0.0.1:8080"; cfg.Server.Address != want {
		t.Fatalf("server.address: want %q got %q", want, cfg.Server.Address)
	}
	if !config.IsExplicitLoopbackListenAddress(cfg.Server.Address) {
		t.Fatalf("expected explicit loopback default, got %q", cfg.Server.Address)
	}
}

func TestLoadFileLoadsBootstrapConfig(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "..", "config", "config.yaml")
	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := routing.ValidateModelAliasesConfig(cfg); err != nil {
		t.Fatalf("routing model_aliases: %v", err)
	}

	if cfg.Server.Address == "" {
		t.Fatal("expected server address")
	}
	if !config.IsExplicitLoopbackListenAddress(cfg.Server.Address) {
		t.Fatalf("expected loopback server address from sample config, got %q", cfg.Server.Address)
	}
	if len(cfg.Plugins.Frontends) == 0 {
		t.Fatal("expected frontend plugin scaffold entries")
	}
	if !cfg.Continuity.InMemory {
		t.Fatal("expected reference config to use in-memory continuity")
	}
	if cfg.Continuity.Store != "memory" {
		t.Fatalf("continuity.store default/normalize: got %q want memory", cfg.Continuity.Store)
	}
}

func TestLoadFile_rejectsModelAliasInvalidPattern(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
server:
  address: "127.0.0.1:0"
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
model_aliases:
  - pattern: "("
    replacement: "stub:m"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if err := routing.ValidateModelAliasesConfig(cfg); err == nil {
		t.Fatal("expected routing validation error")
	}
}

func TestLoadFile_rejectsModelAliasInvalidReplacement(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
server:
  address: "127.0.0.1:0"
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
model_aliases:
  - pattern: "^x$"
    replacement: "|bad"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if err := routing.ValidateModelAliasesConfig(cfg); err == nil {
		t.Fatal("expected routing validation error")
	}
}
