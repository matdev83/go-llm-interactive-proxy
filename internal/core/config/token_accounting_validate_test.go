package config_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

func TestValidateTokenAccountingAcceptsFinalConfigShape(t *testing.T) {
	t.Parallel()
	cfg := validTokenAccountingConfig()
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate() final token accounting config: %v", err)
	}
}

func TestValidateTokenAccountingRejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		mutate  func(*config.Config)
		wantErr string
	}{
		{
			name: "malformed tokenizer mapping empty model",
			mutate: func(cfg *config.Config) {
				cfg.Accounting.Tokenizer.ModelMappings = map[string]string{" ": "cl100k_base"}
			},
			wantErr: "accounting.tokenizer.model_mappings",
		},
		{
			name: "malformed tokenizer mapping empty encoding",
			mutate: func(cfg *config.Config) {
				cfg.Accounting.Tokenizer.ModelMappings = map[string]string{"gpt-test": " "}
			},
			wantErr: "accounting.tokenizer.model_mappings",
		},
		{
			name: "provider required forbids local fallback",
			mutate: func(cfg *config.Config) {
				cfg.Accounting.Mode = "provider_required"
				cfg.Accounting.Tokenizer.DefaultEncoding = "cl100k_base"
			},
			wantErr: "accounting.mode",
		},
		{
			name: "missing sqlite path",
			mutate: func(cfg *config.Config) {
				cfg.Accounting.Ledger.Store = "sqlite"
				cfg.Accounting.Ledger.SQLitePath = ""
			},
			wantErr: "accounting.ledger.sqlite_path",
		},
		{
			name: "missing postgres dsn",
			mutate: func(cfg *config.Config) {
				cfg.Accounting.Ledger.Store = "postgres"
				cfg.Accounting.Ledger.PostgresDSN = ""
			},
			wantErr: "accounting.ledger.postgres_dsn",
		},
		{
			name: "invalid count timeout",
			mutate: func(cfg *config.Config) {
				cfg.Accounting.CountTimeout = "soon"
			},
			wantErr: "accounting.count_timeout",
		},
		{
			name: "insecure admin exposure",
			mutate: func(cfg *config.Config) {
				cfg.Server.Address = "0.0.0.0:8080"
				cfg.Diagnostics.SharedSecret = ""
			},
			wantErr: "diagnostics.shared_secret",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := validTokenAccountingConfig()
			tt.mutate(cfg)
			err := config.Validate(cfg)
			if err == nil {
				t.Fatalf("Validate() error = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func validTokenAccountingConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:0"},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
		Accounting: config.AccountingConfig{
			Enabled:      true,
			Mode:         "provider_first",
			CountTimeout: "750ms",
			Tokenizer: config.AccountingTokenizerConfig{
				DefaultEncoding: "cl100k_base",
				ModelMappings: map[string]string{
					"gpt-test": "o200k_base",
				},
			},
			Preflight: config.AccountingPreflightConfig{
				Mode:                 "required",
				MaxInputTokens:       16_000,
				MaxOutputTokens:      4_000,
				MaxContextTokens:     20_000,
				ClampMaxOutputTokens: true,
			},
			Ledger: config.AccountingLedgerConfig{
				Store:       "memory",
				WritePolicy: "required",
			},
			Admin: config.AccountingAdminConfig{
				Enabled:      true,
				Path:         "/admin/token-count",
				MaxBodyBytes: 4096,
			},
			Observability: config.AccountingObservabilityConfig{Enabled: true},
		},
	}
}
