package traffic

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

// PortBundleFromSnapshot builds emission ports from a frozen runtime snapshot.
func PortBundleFromSnapshot(snap *extensions.RequestRuntimeSnapshot) sdktraffic.PortBundle {
	return snap.TrafficPortBundle()
}
