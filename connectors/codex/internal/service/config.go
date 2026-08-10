package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/acp"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/codex/internal/appserver"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/codex/internal/catalog"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/codex/internal/codex"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

type Config struct {
	BaseURL                                  string                     `yaml:"base_url"`
	AccessToken                              string                     `yaml:"access_token"`
	RefreshToken                             string                     `yaml:"refresh_token"`
	AccountID                                string                     `yaml:"account_id"`
	AuthJSONPath                             string                     `yaml:"auth_json_path"`
	Models                                   []string                   `yaml:"models"`
	Executable                               string                     `yaml:"executable"`
	Model                                    string                     `yaml:"model"`
	ExtraArgs                                []string                   `yaml:"extra_args"`
	DefaultWorkspace                         string                     `yaml:"default_workspace"`
	ConfigOverrides                          []string                   `yaml:"config_overrides"`
	DefaultVerbosity                         string                     `yaml:"default_verbosity"`
	IdleTimeoutS                             float64                    `yaml:"idle_timeout_seconds"`
	StaleKillDelayS                          float64                    `yaml:"stale_kill_delay_seconds"`
	CatalogEnabled                           *bool                      `yaml:"catalog_enabled"`
	CatalogFallbackPath                      string                     `yaml:"catalog_fallback_path"`
	CatalogCodexBinary                       string                     `yaml:"catalog_codex_binary_path"`
	CatalogTimeout                           string                     `yaml:"catalog_timeout"`
	HTTPTimeout                              string                     `yaml:"http_timeout"`
	Transport                                string                     `yaml:"transport"`
	ExperimentalWebSocket                    bool                       `yaml:"experimental_websocket"`
	NativeContext                            *codex.NativeContextConfig `yaml:"-"`
	DisableNativeCompactionWithoutAccounting bool                       `yaml:"-"`
}

type nativeContextYAML struct {
	Enabled                   *bool                         `yaml:"enabled"`
	RequestEncryptedReasoning *bool                         `yaml:"request_encrypted_reasoning"`
	ReasoningContinuity       *codex.ContinuityMode         `yaml:"reasoning_continuity"`
	Compaction                *codex.NativeCompactionConfig `yaml:"compaction"`
}

type configYAML struct {
	Config        `yaml:",inline"`
	NativeContext *nativeContextYAML `yaml:"native_context"`
}

func ParseConfigYAML(kind string, raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		var decoded configYAML
		if err := yaml.Unmarshal(raw, &decoded); err != nil {
			return Config{}, fmt.Errorf("codex connector: config yaml: %w", err)
		}
		cfg = decoded.Config
		if decoded.NativeContext != nil {
			if kind == FactoryKindHTTP {
				defaulted := codex.DefaultNativeContextConfig()
				cfg.NativeContext = &defaulted
				cfg.NativeContext.Source = "explicit"
			} else {
				cfg.NativeContext = &codex.NativeContextConfig{}
			}
			if decoded.NativeContext.Enabled != nil {
				cfg.NativeContext.Enabled = *decoded.NativeContext.Enabled
				cfg.NativeContext.SetEnabledPresentForYAML()
			}
			if decoded.NativeContext.RequestEncryptedReasoning != nil {
				cfg.NativeContext.RequestEncryptedReasoning = *decoded.NativeContext.RequestEncryptedReasoning
				cfg.NativeContext.SetRequestEncryptedPresentForYAML()
			}
			if decoded.NativeContext.ReasoningContinuity != nil {
				cfg.NativeContext.ReasoningContinuity = *decoded.NativeContext.ReasoningContinuity
			}
			if decoded.NativeContext.Compaction != nil {
				cfg.NativeContext.Compaction = *decoded.NativeContext.Compaction
				cfg.NativeContext.SetCompactionPresentForYAML()
			}
		}
		if kind == FactoryKindAppServer {
			// App-server remains default-off; keep its explicit block only so
			// toAppServer can reject non-empty settings deterministically.
		}
	}
	if kind != FactoryKindHTTP && kind != FactoryKindAppServer {
		return Config{}, fmt.Errorf("codex connector: unknown factory kind %q", kind)
	}
	if kind == FactoryKindHTTP && cfg.NativeContext == nil {
		defaulted := codex.DefaultNativeContextConfig()
		cfg.NativeContext = &defaulted
	}
	return cfg, nil
}

func (c Config) catalogLoadOptions() catalog.LoadOptions {
	enabled := true
	if c.CatalogEnabled != nil {
		enabled = *c.CatalogEnabled
	}
	opts := catalog.LoadOptions{
		Enabled:         enabled,
		FallbackPath:    strings.TrimSpace(c.CatalogFallbackPath),
		CodexBinaryPath: strings.TrimSpace(firstNonEmpty(c.CatalogCodexBinary, c.Executable)),
	}
	if d := strings.TrimSpace(c.CatalogTimeout); d != "" {
		if parsed, err := time.ParseDuration(d); err == nil {
			opts.Timeout = parsed
		}
	}
	return opts
}

func (c Config) httpClient() (*http.Client, error) {
	d := 60 * time.Second
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("codex connector: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}

func (c Config) toCodexHTTP(cat *catalog.Catalog) (codex.Config, error) {
	native := c.NativeContext
	if native == nil {
		defaulted := codex.DefaultNativeContextConfig()
		native = &defaulted
	}
	nativeContext, err := normalizeNativeContext(native)
	if err != nil {
		return codex.Config{}, err
	}
	verbosity, err := lipapi.ParseVerbosityLevel(c.DefaultVerbosity)
	if err != nil {
		return codex.Config{}, fmt.Errorf("default_verbosity: %w", err)
	}
	hc, err := c.httpClient()
	if err != nil {
		return codex.Config{}, err
	}
	return codex.Config{
		BaseURL:                                  firstNonEmpty(strings.TrimSpace(c.BaseURL), codex.DefaultBaseURL),
		AccessToken:                              strings.TrimSpace(c.AccessToken),
		RefreshToken:                             strings.TrimSpace(c.RefreshToken),
		AccountID:                                strings.TrimSpace(c.AccountID),
		AuthJSONPath:                             strings.TrimSpace(c.AuthJSONPath),
		HTTPClient:                               hc,
		Models:                                   c.Models,
		ModelCatalog:                             cat,
		DefaultVerbosity:                         verbosity,
		Transport:                                strings.TrimSpace(c.Transport),
		ExperimentalWebSocket:                    c.ExperimentalWebSocket,
		NativeContext:                            nativeContext,
		DisableNativeCompactionWithoutAccounting: c.DisableNativeCompactionWithoutAccounting,
	}, nil
}

func (c Config) toAppServer(cat *catalog.Catalog, src catalog.Source, cache *acp.ExecutableCache) (appserver.Config, error) {
	if c.NativeContext != nil {
		if _, err := normalizeNativeContext(c.NativeContext); err != nil {
			return appserver.Config{}, err
		}
		if c.NativeContext.HasNonDefaultSettings() {
			return appserver.Config{}, fmt.Errorf("codex connector: native_context is not supported for app-server connector")
		}
	}
	verbosity, err := lipapi.ParseVerbosityLevel(c.DefaultVerbosity)
	if err != nil {
		return appserver.Config{}, fmt.Errorf("default_verbosity: %w", err)
	}
	cfg := appserver.Config{
		ConnectorConfig: acp.ConnectorConfig{
			Executable:       strings.TrimSpace(c.Executable),
			Model:            strings.TrimSpace(c.Model),
			ExtraArgs:        c.ExtraArgs,
			DefaultWorkspace: strings.TrimSpace(c.DefaultWorkspace),
		},
		ConfigOverrides:    c.ConfigOverrides,
		ModelCatalog:       cat,
		ModelCatalogSource: src,
		DefaultVerbosity:   verbosity,
		ExeCache:           cache,
	}
	if c.IdleTimeoutS > 0 {
		cfg.IdleTimeout = time.Duration(c.IdleTimeoutS * float64(time.Second))
	}
	if c.StaleKillDelayS > 0 {
		cfg.StaleKillDelay = time.Duration(c.StaleKillDelayS * float64(time.Second))
	}
	return cfg, nil
}

func normalizeNativeContext(cfg *codex.NativeContextConfig) (*codex.NativeContextConfig, error) {
	if cfg == nil {
		defaulted := codex.DefaultNativeContextConfig()
		cfg = &defaulted
	}
	norm, err := cfg.NormalizeAndValidate()
	if err != nil {
		return nil, fmt.Errorf("codex connector: native_context: %w", err)
	}
	return &norm, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
