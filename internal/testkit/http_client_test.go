package testkit

import (
	"net/http"
	"testing"
)

func TestIntegrationHTTPClient_nonNilPassthrough(t *testing.T) {
	t.Parallel()
	c := &http.Client{}
	if got := IntegrationHTTPClient(c); got != c {
		t.Fatalf("want same client pointer")
	}
}

func TestIntegrationHTTPClient_nilIsDefault(t *testing.T) {
	t.Parallel()
	if got := IntegrationHTTPClient(nil); got != http.DefaultClient {
		t.Fatalf("nil arg should select http.DefaultClient")
	}
}
