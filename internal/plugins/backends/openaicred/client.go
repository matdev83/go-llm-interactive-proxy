package openaicred

import (
	"net/http"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// NewOpenAIClient builds an openai-go client for the given base URL and API secret.
// httpClient and maxRetries may be nil / omitted for SDK defaults.
func NewOpenAIClient(baseURL, apiSecret string, httpClient *http.Client, maxRetries *int) openai.Client {
	return NewOpenAIClientWithOptions(baseURL, apiSecret, httpClient, maxRetries, nil)
}

// NewOpenAIClientWithOptions builds an openai-go client with additional per-client request options.
// extraOpts are appended after base URL, API key, HTTP client, and max retries.
func NewOpenAIClientWithOptions(baseURL, apiSecret string, httpClient *http.Client, maxRetries *int, extraOpts []option.RequestOption) openai.Client {
	opts := make([]option.RequestOption, 0, 4+len(extraOpts))
	opts = append(opts,
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiSecret),
	)
	if httpClient != nil {
		opts = append(opts, option.WithHTTPClient(httpClient))
	}
	if maxRetries != nil {
		opts = append(opts, option.WithMaxRetries(*maxRetries))
	}
	opts = append(opts, extraOpts...)
	return openai.NewClient(opts...)
}
