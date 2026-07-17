package httpauth

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"

// IngressAttribution is a transport alias for [secretguard.IngressAttribution]
// so auth middleware and core share one type without core importing this package.
type IngressAttribution = secretguard.IngressAttribution
