//go:build !windows && !linux && !darwin

package trust

import (
	"fmt"
	"os"

	sdkmanifest "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/manifest"
)

func bindVerified(f *os.File, _ sdkmanifest.Manifest, _ string, _ VerifyOptions) VerifyResult {
	if f != nil {
		_ = f.Close()
	}
	return VerifyResult{Reason: ReasonStagingUnsupported, Err: fmt.Errorf("unsupported GOOS")}
}
