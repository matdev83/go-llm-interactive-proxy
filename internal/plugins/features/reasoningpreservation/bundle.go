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

func FeatureBundleWithCompanionPolicy(cfg Config, policy CompanionPolicy) (lipfeature.FeatureBundle, error) {
	_, b, err := FeatureBundleWithPartsAndPolicy(cfg, policy)
	return b, err
}

// FeatureBundleWithParts returns the shared store/telemetry participants plus the schema-V1 bundle.
// Disabled configurations must not call this constructor (D12).
func FeatureBundleWithParts(cfg Config) (*InstanceParts, lipfeature.FeatureBundle, error) {
	return FeatureBundleWithPartsAndPolicy(cfg, CompanionPolicy{})
}

func FeatureBundleWithPartsAndPolicy(cfg Config, policy CompanionPolicy) (*InstanceParts, lipfeature.FeatureBundle, error) {
	return FeatureBundleWithPartsAndCompression(cfg, CompressionServices{}, policy)
}

// FeatureBundleWithPartsAndCompression extends the feature composition with explicit
// generation-local compression capabilities. It wires validated
// CompressionConfig.ToLimits() into StoreOptions.CompressionLimits and validates
// that enabled compression has all required capabilities, while disabled mode
// requires none (zero delta).
func FeatureBundleWithPartsAndCompression(cfg Config, svc CompressionServices, policy CompanionPolicy) (*InstanceParts, lipfeature.FeatureBundle, error) {
	if err := svc.validateFor(cfg); err != nil {
		return nil, lipfeature.FeatureBundle{}, err
	}
	store, err := NewMemoryTurnStore(StoreOptions{
		TTL:                      cfg.State.TTL,
		MaxTurnsPerSession:       cfg.State.MaxTurnsPerSession,
		MaxReasoningBytesPerTurn: cfg.State.MaxReasoningBytesPerTurn,
		MaxSessionBytes:          cfg.State.MaxSessionBytes,
		CompressionLimits:        cfg.Compression.ToLimits(),
	})
	if err != nil {
		return nil, lipfeature.FeatureBundle{}, err
	}
	tel := NewTelemetry()
	stage := CompletedAdoptionStage(identityAdoptionStage)
	if cfg.Compression.Enabled {
		if cs, ok := store.(CompressionStore); ok {
			stage = NewDecoderAdoptionStage(cfg, cs, svc, tel)
		}
	}
	xform := NewAttemptTransformWithCompanionPolicyServicesAndStage(cfg, store, svc, policy, stage, tel)
	hook := BuildPostAppendHook(cfg, store, svc)
	obs := NewStreamObserverFactoryWithPostAppendHook(cfg, store, hook, tel)
	b := lipfeature.FeatureBundle{
		SchemaVersion:           lipfeature.SchemaVersionV1,
		AttemptTransforms:       []request.AttemptTransform{xform},
		StreamObserverFactories: []response.StreamObserverFactory{obs},
	}
	if err := b.Validate(); err != nil {
		return nil, lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", ID, err)
	}
	parts := &InstanceParts{
		Config:              cfg,
		Store:               store,
		Telemetry:           tel,
		Transform:           xform,
		Observer:            obs,
		CompressionServices: svc,
	}
	return parts, b, nil
}

// FeatureBundleWithCompression is a convenience wrapper without companion policy.
func FeatureBundleWithCompression(cfg Config, svc CompressionServices) (*InstanceParts, lipfeature.FeatureBundle, error) {
	return FeatureBundleWithPartsAndCompression(cfg, svc, CompanionPolicy{})
}

// InstanceParts exposes test/diagnostics handles for one enabled feature instance.
type InstanceParts struct {
	Config              Config
	Store               TurnStore
	Telemetry           *Telemetry
	Transform           *AttemptTransform
	Observer            *StreamObserverFactory
	CompressionServices CompressionServices
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
