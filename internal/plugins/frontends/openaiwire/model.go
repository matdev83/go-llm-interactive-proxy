package openaiwire

import (
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ModelFromExtensions returns the wire model string stored under extKey during decode.
func ModelFromExtensions(c *lipapi.Call, extKey string) string {
	if c == nil || c.Extensions == nil || extKey == "" {
		return ""
	}
	raw, ok := c.Extensions[extKey]
	if !ok || len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	return ""
}
