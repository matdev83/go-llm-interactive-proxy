package bundle_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/bundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/config"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"gopkg.in/yaml.v3"
)

func BenchmarkBundle_OptionalTelemetry(b *testing.B) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("enabled: true\nmode: rewrite\n"), &node); err != nil {
		b.Fatal(err)
	}
	resolved, err := config.Decode(node)
	if err != nil {
		b.Fatal(err)
	}
	for _, record := range []bool{false, true} {
		name := "stock"
		if record {
			name = "reader_enabled"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var result lipfeature.FeatureBundle
				var err error
				if record {
					_, result, err = bundle.FeatureBundleWithTelemetry(resolved)
				} else {
					result, err = bundle.FeatureBundle(resolved)
				}
				if err != nil {
					b.Fatal(err)
				}
				if len(lipfeature.Get(result.PlaneSet, lipfeature.PlaneAttemptTransforms)) != 1 {
					b.Fatal("enabled bundle lost its attempt transform")
				}
			}
		})
	}
}
