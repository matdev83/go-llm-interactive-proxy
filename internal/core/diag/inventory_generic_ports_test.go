package diag

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"gopkg.in/yaml.v3"
)

type genericPortRegistry struct {
	bundle lipfeature.FeatureBundle
}

func (r genericPortRegistry) BuildFeatureBundle(string, yaml.Node) (lipfeature.FeatureBundle, error) {
	return r.bundle, nil
}

func TestBuildInventoryExtensions_genericPortAggregatePosture(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{
				{ID: "feat-a", Enabled: true},
				{ID: "feat-b", Enabled: true},
			},
		},
	}
	cs := lipfeature.NewContributionSet()
	_ = lipfeature.Contribute(cs, lipfeature.PlaneAttemptTransforms, "test", []request.AttemptTransform{
		invAttemptTransform{id: "at-a", ord: 1},
	})
	_ = lipfeature.Contribute(cs, lipfeature.PlaneStreamObserverFactories, "test", []response.StreamObserverFactory{
		invStreamObserverFactory{id: "so-a", ord: 1},
		invStreamObserverFactory{id: "so-b", ord: 2},
	})
	reg := genericPortRegistry{bundle: lipfeature.BundleFromPlanes(cs.Freeze(), nil)}
	ext := buildInventoryExtensions(context.Background(), cfg, &InventoryExtras{
		Reg: reg,
		Registrations: []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "feat-a", Enabled: true, FactoryKind: "x"},
			{Kind: lipsdk.PluginKindFeature, ID: "feat-b", Enabled: true, FactoryKind: "x"},
		},
	})
	if !ext.GenericPorts.AttemptTransformOccupied || ext.GenericPorts.AttemptTransformHandlers != 2 {
		t.Fatalf("attempt posture=%#v", ext.GenericPorts)
	}
	if !ext.GenericPorts.FinalStreamObservationOccupied || ext.GenericPorts.FinalStreamObservationHandlers != 4 {
		t.Fatalf("observer posture=%#v", ext.GenericPorts)
	}
	var atOcc, soOcc bool
	for _, f := range ext.Features {
		for _, occ := range f.StageOccupancy {
			switch occ.StageID {
			case extensions.StageCandidateAttemptTransform:
				atOcc = true
			case extensions.StageFinalStreamObservation:
				soOcc = true
			}
		}
	}
	if !atOcc || !soOcc {
		t.Fatalf("per-feature occupancy missing at=%v so=%v", atOcc, soOcc)
	}
}

func TestBuildInventoryExtensions_absentPortsZeroPosture(t *testing.T) {
	t.Parallel()
	ext := buildInventoryExtensions(context.Background(), &config.Config{}, nil)
	if ext.GenericPorts.AttemptTransformOccupied || ext.GenericPorts.AttemptTransformHandlers != 0 {
		t.Fatalf("absent attempt posture=%#v", ext.GenericPorts)
	}
	if ext.GenericPorts.FinalStreamObservationOccupied || ext.GenericPorts.FinalStreamObservationHandlers != 0 {
		t.Fatalf("absent observer posture=%#v", ext.GenericPorts)
	}
}

func TestBuildInventoryExtensions_genericPortPosturePrivacyNoSensitiveFields(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{ID: "feat-priv", Enabled: true}},
		},
	}
	cs := lipfeature.NewContributionSet()
	_ = lipfeature.Contribute(cs, lipfeature.PlaneAttemptTransforms, "test", []request.AttemptTransform{
		invAttemptTransform{id: "keep", ord: 1},
	})
	_ = lipfeature.Contribute(cs, lipfeature.PlaneStreamObserverFactories, "test", []response.StreamObserverFactory{
		invStreamObserverFactory{id: "keep", ord: 1},
	})
	reg := genericPortRegistry{bundle: lipfeature.BundleFromPlanes(cs.Freeze(), nil)}
	ext := buildInventoryExtensions(context.Background(), cfg, &InventoryExtras{
		Reg: reg,
		Registrations: []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "feat-priv", Enabled: true, FactoryKind: "x"},
		},
	})
	raw, err := json.Marshal(ext)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ToLower(string(raw))
	for _, forbidden := range []string{
		"reasoning_text",
		"session_partition",
		"prompt_excerpt",
		`"anchor"`,
		`"payload"`,
		`"opaque"`,
		`"signature"`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("inventory JSON must not contain %q; body=%s", forbidden, body)
		}
	}
}
