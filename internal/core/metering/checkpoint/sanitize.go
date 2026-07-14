package checkpoint

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

// SanitizeCall clears resume secrets and other bearer material from a Call clone
// so memory-held metering snapshots never retain raw resume tokens (req 2.7).
func SanitizeCall(c lipapi.Call) lipapi.Call {
	c.Session.ResumeToken = ""
	return c
}
