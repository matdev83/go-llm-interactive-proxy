package runtimebundle

import (
	"context"
	"net/http"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/codexcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"gopkg.in/yaml.v3"
)

func TestHasEnabledRegisteredCodexCatalogConsumer(t *testing.T) {
	t.Parallel()
	withFactory := func(id string) *pluginreg.Registry {
		t.Helper()
		reg := pluginreg.NewRegistry()
		if err := reg.RegisterBackend(id, func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
			return execbackend.Backend{}, nil
		}); err != nil {
			t.Fatal(err)
		}
		return reg
	}
	for _, tt := range []struct {
		name string
		cfg  *config.Config
		reg  *pluginreg.Registry
		want bool
	}{
		{name: "nil config", cfg: nil, reg: pluginreg.NewRegistry(), want: false},
		{name: "nil registry", cfg: &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			Kind: "openai-codex", ID: "codex", Enabled: true,
		}}}}, reg: nil, want: false},
		{
			name: "enabled registered openai-codex",
			cfg: &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
				Kind: "openai-codex", ID: "codex", Enabled: true,
			}}}},
			reg:  withFactory("openai-codex"),
			want: true,
		},
		{
			name: "enabled registered app-server",
			cfg: &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
				Kind: "openai-codex-app-server", ID: "app", Enabled: true,
			}}}},
			reg:  withFactory("openai-codex-app-server"),
			want: true,
		},
		{
			name: "enabled unregistered openai-codex",
			cfg: &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
				Kind: "openai-codex", ID: "codex", Enabled: true,
			}}}},
			reg:  pluginreg.NewRegistry(),
			want: false,
		},
		{
			name: "disabled registered openai-codex",
			cfg: &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
				Kind: "openai-codex", ID: "codex", Enabled: false,
			}}}},
			reg:  withFactory("openai-codex"),
			want: false,
		},
		{
			name: "unrelated enabled registered backend",
			cfg: &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
				Kind: "vllm", ID: "vllm", Enabled: true,
			}}}},
			reg:  withFactory("vllm"),
			want: false,
		},
		{
			name: "id-only openai-codex factory registered",
			cfg: &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
				ID: "openai-codex", Enabled: true,
			}}}},
			reg:  withFactory("openai-codex"),
			want: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := hasEnabledRegisteredCodexCatalogConsumer(tt.cfg, tt.reg); got != tt.want {
				t.Fatalf("hasEnabledRegisteredCodexCatalogConsumer() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadCodexModelCatalog_nilLoggerDoesNotPanic(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("openai-codex", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
		Kind: "openai-codex", ID: "codex", Enabled: true,
	}}}}
	cat, err := codexcatalog.LoadFallback("")
	if err != nil {
		t.Fatal(err)
	}
	loadFn := func(context.Context, codexcatalog.LoadOptions) (*codexcatalog.Catalog, codexcatalog.Source, error) {
		return cat, codexcatalog.SourceDiscovered, nil
	}
	got, src := loadCodexModelCatalog(context.Background(), cfg, reg, nil, loadFn)
	if got == nil {
		t.Fatal("expected non-nil catalog with nil logger")
	}
	if src != codexcatalog.SourceDiscovered {
		t.Fatalf("source = %q, want %q", src, codexcatalog.SourceDiscovered)
	}
}
