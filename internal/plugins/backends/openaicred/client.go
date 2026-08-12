package openaicred

import (
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// NewClient builds an openai-go client for the given base URL and API secret.
// httpClient and maxRetries may be nil / omitted for SDK defaults.
func NewClient(baseURL, apiSecret string, httpClient *http.Client, maxRetries *int) openai.Client {
	return NewClientWithOptions(baseURL, apiSecret, httpClient, maxRetries, nil)
}

// NewClientWithOptions builds an openai-go client with additional per-client request options.
// extraOpts are appended after base URL, API key, HTTP client, and max retries.
// An empty apiSecret omits Authorization entirely (compatible-mode no-auth); non-empty
// secrets including native dummy credentials still set option.WithAPIKey.
func NewClientWithOptions(baseURL, apiSecret string, httpClient *http.Client, maxRetries *int, extraOpts []option.RequestOption) openai.Client {
	opts := make([]option.RequestOption, 0, 4+len(extraOpts))
	opts = append(opts, option.WithBaseURL(baseURL))
	if secret := strings.TrimSpace(apiSecret); secret != "" {
		opts = append(opts, option.WithAPIKey(secret))
	}
	if httpClient != nil {
		opts = append(opts, option.WithHTTPClient(httpClient))
	}
	if maxRetries != nil {
		opts = append(opts, option.WithMaxRetries(*maxRetries))
	}
	opts = append(opts, extraOpts...)
	return openai.NewClient(opts...)
}
