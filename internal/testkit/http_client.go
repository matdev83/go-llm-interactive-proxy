package testkit

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
)

// IntegrationHTTPClient returns c when non-nil; otherwise it returns [httpclient.Standard]
// so integration tests match production outbound pooling and timeouts (not [http.DefaultClient]).
func IntegrationHTTPClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return httpclient.Standard()
}
