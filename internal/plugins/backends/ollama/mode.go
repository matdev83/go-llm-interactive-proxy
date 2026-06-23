package ollama

import "strings"

type backendMode int

const (
	backendModeLocal backendMode = iota
	backendModeCloud
)

func discoveryLocalForMode(mode backendMode, d DiscoveryConfig) bool {
	if !DiscoveryEnabled(d) {
		return false
	}
	if mode != backendModeLocal {
		return false
	}
	if d.Local != nil {
		return *d.Local
	}
	return true
}

func discoveryCloudForMode(mode backendMode, d DiscoveryConfig) bool {
	if !DiscoveryEnabled(d) {
		return false
	}
	if mode != backendModeCloud {
		return false
	}
	if d.Cloud != nil {
		return *d.Cloud
	}
	return true
}

const fallbackCanonicalVendor = "unknown"

func fallbackCanonicalID(mode backendMode, native string) string {
	native = stringsTrimCloudSuffixForCanonical(mode, native)
	if native == "" {
		return fallbackCanonicalVendor + "/unknown"
	}
	return fallbackCanonicalVendor + "/" + native
}

func stringsTrimCloudSuffixForCanonical(mode backendMode, native string) string {
	native = strings.TrimSpace(native)
	if mode == backendModeCloud {
		native = strings.TrimSuffix(native, "-cloud")
	}
	return native
}
