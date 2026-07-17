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
	store, err := NewMemoryTurnStore(StoreOptions{
		TTL:                      cfg.State.TTL,
		MaxTurnsPerSession:       cfg.State.MaxTurnsPerSession,
		MaxReasoningBytesPerTurn: cfg.State.MaxReasoningBytesPerTurn,
		MaxSessionBytes:          cfg.State.MaxSessionBytes,
	})
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	xform := NewAttemptTransform(cfg, store)
	obs := NewStreamObserverFactory(cfg, store)
	b := lipfeature.FeatureBundle{
		SchemaVersion:           lipfeature.SchemaVersionV1,
		AttemptTransforms:       []request.AttemptTransform{xform},
		StreamObserverFactories: []response.StreamObserverFactory{obs},
	}
	if err := b.Validate(); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", ID, err)
	}
	return b, nil
}

// BuildFeatureBundle decodes YAML and returns a FeatureBundle for registry factories.
func BuildFeatureBundle(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	return FeatureBundle(cfg)
}
