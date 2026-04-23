package diag

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"gopkg.in/yaml.v3"
)

type failRegistry struct{}

func (failRegistry) BuildFeatureBundle(string, yaml.Node) (lipfeature.FeatureBundle, error) {
	return lipfeature.FeatureBundle{}, errors.New("factory boom")
}

func TestBuildInventoryExtensions_bundleErrorOnFactoryFailure(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{ID: "submit-noop", Enabled: true}},
		},
	}
	ext := buildInventoryExtensions(context.Background(), cfg, &InventoryExtras{
		Reg:           failRegistry{},
		Registrations: nil,
	})
	if len(ext.Features) != 1 {
		t.Fatalf("features: %d", len(ext.Features))
	}
	if ext.Features[0].BundleError == "" {
		t.Fatal("expected bundle_error when factory fails")
	}
	if len(ext.Features[0].StageOccupancy) != 0 {
		t.Fatal("expected empty occupancy on factory error")
	}
}

func TestBuildInventoryExtensions_contextCanceledSkipsFactory(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{ID: "submit-noop", Enabled: true}},
		},
	}
	ext := buildInventoryExtensions(ctx, cfg, &InventoryExtras{
		Reg:           failRegistry{},
		Registrations: nil,
	})
	if len(ext.Features) != 1 {
		t.Fatalf("features: %d", len(ext.Features))
	}
	if ext.Features[0].BundleError == "" {
		t.Fatal("expected bundle_error when ctx is canceled")
	}
}

func TestBuildInventoryExtensions_bundleErrorOnRegistrationMismatch(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{ID: "submit-noop", Enabled: true}},
		},
	}
	ext := buildInventoryExtensions(context.Background(), cfg, &InventoryExtras{
		Reg: failRegistry{},
		Registrations: []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "other-instance", Enabled: true},
		},
	})
	if len(ext.Features) != 1 {
		t.Fatalf("features: %d", len(ext.Features))
	}
	if ext.Features[0].BundleError == "" {
		t.Fatal("expected bundle_error when registration row missing")
	}
}
