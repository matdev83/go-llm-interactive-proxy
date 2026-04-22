package testkit

import "net/http"

// IntegrationHTTPClient returns c when non-nil; otherwise it returns the shared client used for
// black-box HTTP integration tests (same behavior as http.DefaultClient).
func IntegrationHTTPClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return http.DefaultClient
}
