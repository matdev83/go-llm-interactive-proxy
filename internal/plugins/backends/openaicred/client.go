package openaicred

import (
	"net/http"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// NewOpenAIClient builds an openai-go client for the given base URL and API secret.
// httpClient and maxRetries may be nil / omitted for SDK defaults.
func NewOpenAIClient(baseURL, apiSecret string, httpClient *http.Client, maxRetries *int) openai.Client {
	opts := make([]option.RequestOption, 0, 3)
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
	return openai.NewClient(opts...)
}
