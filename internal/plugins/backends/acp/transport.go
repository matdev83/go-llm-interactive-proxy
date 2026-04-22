package acp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// JSON-RPC unary responses should be small; cap read size to avoid unbounded
// allocation from a buggy or hostile ACP endpoint (aligned with frontend
// request body caps in spirit).
const maxUnaryHTTPResponseBytes = 8 << 20

// Error and non-OK response bodies only need a short snippet for diagnostics.
const maxErrorSnippetBytes = 8192

// DefaultHTTPClientTimeout is the Timeout applied to a dedicated [*http.Client]
// allocated when [newHTTPTransport] receives a nil client.
const DefaultHTTPClientTimeout = 120 * time.Second

func readHTTPBodyLimited(r io.ReadCloser, max int) ([]byte, error) {
	defer func() { _ = r.Close() }()
	lr := io.LimitReader(r, int64(max)+1)
	b, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if len(b) > max {
		_, _ = io.Copy(io.Discard, r)
		return nil, fmt.Errorf("acp: response body exceeds %d bytes", max)
	}
	return b, nil
}

// Transport abstracts how JSON-RPC is exchanged with an ACP agent. The HTTP
// implementation matches internal/refbackend/acp; a future stdio Conn can implement
// the same surface for newline-delimited JSON-RPC.
type Transport interface {
	// CallUnary posts one JSON-RPC request and reads the full response body when
	// status matches expectStatus (typically 200). For 204, body may be empty.
	CallUnary(ctx context.Context, body []byte, expectStatus int) ([]byte, error)
	// CallPromptStream posts session/prompt and returns the response body stream (NDJSON).
	CallPromptStream(ctx context.Context, body []byte) (io.ReadCloser, error)
	// SendJSONRPC posts an arbitrary JSON-RPC object (e.g. response to an inbound
	// server request during a prompt on HTTP transports that use POST per message).
	SendJSONRPC(ctx context.Context, body []byte) error
}

// httpTransport implements Transport over POST {origin}/v1/acp (batch HTTP).
type httpTransport struct {
	endpoint string
	hc       *http.Client
}

// newHTTPTransport builds an HTTP [Transport] for POST {origin}/v1/acp.
// When hc is nil, a new [*http.Client] is allocated with Timeout [DefaultHTTPClientTimeout].
// When hc is non-nil, it is used as-is.
func newHTTPTransport(baseURL string, hc *http.Client) (*httpTransport, error) {
	u := strings.TrimSpace(baseURL)
	if u == "" {
		return nil, fmt.Errorf("acp: BaseURL is required")
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return nil, fmt.Errorf("acp: BaseURL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("acp: BaseURL must include scheme and host")
	}
	if hc == nil {
		hc = &http.Client{
			Timeout: DefaultHTTPClientTimeout,
		}
	}
	u2 := *parsed
	u2.Path = strings.TrimSuffix(u2.Path, "/") + "/v1/acp"
	u2.RawQuery = ""
	u2.Fragment = ""
	return &httpTransport{endpoint: u2.String(), hc: hc}, nil
}

func (t *httpTransport) CallUnary(ctx context.Context, body []byte, expectStatus int) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != expectStatus {
		snippet, err := readHTTPBodyLimited(resp.Body, maxErrorSnippetBytes)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("acp: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return readHTTPBodyLimited(resp.Body, maxUnaryHTTPResponseBytes)
}

func (t *httpTransport) CallPromptStream(ctx context.Context, body []byte) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		b, err := readHTTPBodyLimited(resp.Body, maxErrorSnippetBytes)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("acp: session/prompt: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp.Body, nil
}

func (t *httpTransport) SendJSONRPC(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.hc.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		b, err := readHTTPBodyLimited(resp.Body, maxErrorSnippetBytes)
		if err != nil {
			return err
		}
		return fmt.Errorf("acp: SendJSONRPC: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
