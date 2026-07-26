package manifest_test

import (
	"testing"

	inframanifest "github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/manifest"
)

func FuzzManifest(f *testing.F) {
	f.Add([]byte(validJSON()))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schema":"golip.backendplugin.manifest/v1"}`))
	f.Add([]byte(`{"env":{}}`))
	f.Fuzz(func(t *testing.T, in []byte) {
		_, _ = inframanifest.ParseStrictBytes(in)
	})
}
