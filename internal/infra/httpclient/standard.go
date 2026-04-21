package httpclient

import (
	"net"
	"net/http"
	"time"
)

// Standard returns an HTTP client suitable for upstream provider calls from the standard bundle:
// explicit transport (timeouts, pooling), not package-global DefaultClient.
func Standard() *http.Client {
	return &http.Client{
		Transport: DefaultTransport(),
		Timeout:   120 * time.Second,
	}
}

// DefaultTransport builds a shared transport policy for outbound HTTP from the composition root.
func DefaultTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}
