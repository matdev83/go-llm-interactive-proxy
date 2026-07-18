package reasoningpreservation

import (
	"fmt"

	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"gopkg.in/yaml.v3"
)

// FeatureBundle builds the schema-V1 contribution for an enabled feature instance.
func FeatureBundle(cfg Config) (lipfeature.FeatureBundle, error) {
	_, b, err := FeatureBundleWithParts(cfg)
	return b, err
}

// FeatureBundleWithParts returns the shared store/telemetry participants plus the schema-V1 bundle.
// Disabled configurations must not call this constructor (D12).
func FeatureBundleWithParts(cfg Config) (*InstanceParts, lipfeature.FeatureBundle, error) {
	store, err := NewMemoryTurnStore(StoreOptions{
		TTL:                      cfg.State.TTL,
		MaxTurnsPerSession:       cfg.State.MaxTurnsPerSession,
		MaxReasoningBytesPerTurn: cfg.State.MaxReasoningBytesPerTurn,
		MaxSessionBytes:          cfg.State.MaxSessionBytes,
	})
	if err != nil {
		return nil, lipfeature.FeatureBundle{}, err
	}
	tel := NewTelemetry()
	xform := NewAttemptTransform(cfg, store, tel)
	obs := NewStreamObserverFactory(cfg, store, tel)
	b := lipfeature.FeatureBundle{
		SchemaVersion:           lipfeature.SchemaVersionV1,
		AttemptTransforms:       []request.AttemptTransform{xform},
		StreamObserverFactories: []response.StreamObserverFactory{obs},
	}
	if err := b.Validate(); err != nil {
		return nil, lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", ID, err)
	}
	parts := &InstanceParts{
		Config:    cfg,
		Store:     store,
		Telemetry: tel,
		Transform: xform,
		Observer:  obs,
	}
	return parts, b, nil
}

// InstanceParts exposes test/diagnostics handles for one enabled feature instance.
type InstanceParts struct {
	Config    Config
	Store     TurnStore
	Telemetry *Telemetry
	Transform *AttemptTransform
	Observer  *StreamObserverFactory
}

func (p *InstanceParts) Inventory() SafeInventory {
	if p == nil {
		return SafeInventory{}
	}
	return BuildSafeInventory(p.Config, p.Telemetry)
}

// BuildFeatureBundle decodes YAML and returns a FeatureBundle for registry factories.
func BuildFeatureBundle(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	return FeatureBundle(cfg)
}
