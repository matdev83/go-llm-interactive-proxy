package openresponsescompat

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestSanitizeHTTPTransportError_RemovesQuerySecrets(t *testing.T) {
	const secret = "sk-query-secret"
	original := &url.Error{
		Op:  "Post",
		URL: "https://provider.example/v1/responses?api_key=" + secret + "&token=also-secret",
		Err: errors.New("connection refused"),
	}

	got := sanitizeHTTPTransportError(fmt.Errorf("wrapped transport failure: %w", original))
	var sanitized *url.Error
	if !errors.As(got, &sanitized) {
		t.Fatalf("sanitized error = %T %v, want *url.Error", got, got)
	}
	if sanitized.URL != "https://provider.example/v1/responses" {
		t.Fatalf("sanitized URL = %q, want query-free endpoint", sanitized.URL)
	}
	if strings.Contains(got.Error(), secret) || strings.Contains(got.Error(), "also-secret") {
		t.Fatalf("sanitized error leaked query credentials: %v", got)
	}
}
