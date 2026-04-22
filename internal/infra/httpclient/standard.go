package httpclient

import (
	"net"
	"net/http"
	"time"
)

// Outbound tuning for shared upstream calls (LLM APIs). Go's default MaxIdleConnsPerHost is 2,
// which causes connection churn under concurrency to a single host; we raise it explicitly.
const (
	defaultMaxIdleConnsPerHost = 64
	// ResponseHeaderTimeout bounds time waiting for response headers (distinct from Client.Timeout,
	// which covers the full round trip including body). Slightly below Standard's client timeout.
	defaultResponseHeaderTimeout = 60 * time.Second
)

// Standard returns an HTTP client suitable for upstream provider calls from the standard bundle:
// explicit transport (timeouts, pooling), not package-global DefaultClient.
// It honors HTTP_PROXY / HTTPS_PROXY from the process environment.
func Standard() *http.Client {
	return StandardWithTrustEnvironment(true)
}

// StandardWithTrustEnvironment is like [Standard] but allows disabling use of proxy-related
// environment variables (sets Transport.Proxy to nil when trustEnv is false).
func StandardWithTrustEnvironment(trustEnv bool) *http.Client {
	return &http.Client{
		Transport: NewTransport(trustEnv),
		Timeout:   120 * time.Second,
	}
}

// DefaultTransport builds a shared transport policy for outbound HTTP from the composition root.
// It honors HTTP_PROXY / HTTPS_PROXY from the environment.
func DefaultTransport() *http.Transport {
	return NewTransport(true)
}

// NewTransport returns a new transport with the same tuning as [DefaultTransport].
// When trustEnvironmentProxy is false, Proxy is nil so HTTP_PROXY and related env vars are ignored.
func NewTransport(trustEnvironmentProxy bool) *http.Transport {
	t := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:       true,
		MaxIdleConns:            100,
		MaxIdleConnsPerHost:     defaultMaxIdleConnsPerHost,
		IdleConnTimeout:         90 * time.Second,
		TLSHandshakeTimeout:     10 * time.Second,
		ExpectContinueTimeout:   1 * time.Second,
		ResponseHeaderTimeout:   defaultResponseHeaderTimeout,
	}
	if trustEnvironmentProxy {
		t.Proxy = http.ProxyFromEnvironment
	} else {
		t.Proxy = nil
	}
	return t
}
