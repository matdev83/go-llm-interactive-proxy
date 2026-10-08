package archtest

import "github.com/matdev83/go-llm-interactive-proxy/internal/featureplanegen"

// GenerateFeaturePlanesCode parses the feature-plane manifest and returns its generated Go source.
func GenerateFeaturePlanesCode(manifestBytes []byte) ([]byte, error) {
	return featureplanegen.GenerateFeaturePlanesCode(manifestBytes)
}

// WriteGeneratedFileAtomic atomically installs generated feature-plane source.
func WriteGeneratedFileAtomic(path string, data []byte) error {
	return featureplanegen.WriteGeneratedFileAtomic(path, data)
}
