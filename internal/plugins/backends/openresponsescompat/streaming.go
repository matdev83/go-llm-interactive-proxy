package openresponsescompat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/compatmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/streampeek"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/transporterr"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// openCreateStreaming maps an item-authoritative create call to a context-aware
// SSE POST (stream:true, Accept text/event-stream) and returns a committed
// canonical stream. It peeks the first canonical event before returning so any
// pre-output transport or protocol failure is retryable by core; once the first
// event has been delivered, later failures remain stream errors and the caller
// can no longer fail over. This adapter never retries upstream itself.
func openCreateStreaming(ctx context.Context, id string, spec BackendSpec, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
	body, err := buildCreateRequestBody(id, spec, call, cand, true)
	if err != nil {
		return nil, err
	}
	endpointURL, err := resolveCreateEndpoint(spec.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	apiKey := compatmode.FirstAPIKey(compatmode.ResolveEnvAPIKeys(spec.APIKeyEnvVarRoot))
	resp, err := doStreaming(ctx, spec.HTTPClient, endpointURL, body, apiKey)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}

	es := newSSEStream(id, resp.Body, spec.ResponseLimits, call.MaxPendingWireEvents)
	streamReady := false
	defer func() {
		if !streamReady {
			_ = es.Close()
		}
	}()
	ev, err := es.Recv(ctx)
	if err != nil {
		cerr := es.Close()
		if errors.Is(err, io.EOF) {
			err = fmt.Errorf("%s: %w: SSE stream produced no canonical events", id, ErrMalformedResponse)
		}
		return nil, errors.Join(classifyStreamOpenError(err), cerr)
	}
	if ev.Kind == lipapi.EventError {
		_ = es.Close()
		cause := errors.Join(
			lipapi.NewStreamError(ev.ErrorCode, ev.ErrorMessage),
			fmt.Errorf("%s: %w: upstream returned an error before output", id, ErrMalformedResponse),
		)
		return nil, lipapi.RecoverablePreOutputError(cause)
	}
	streamReady = true
	return streampeek.NewManagedPrependFirst(ev, es), nil
}

// classifyStreamOpenError marks a stream-open failure retryable so core can
// fail over before commitment. Context cancellation/deadline is never retried.
func classifyStreamOpenError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if lipapi.IsRecoverablePreOutput(err) {
		return err
	}
	return lipapi.RecoverablePreOutputError(err)
}

// doStreaming performs the context-aware POST JSON create with stream:true and
// strict status/content-type validation. On success the caller owns resp.Body
// (closed exactly once by the managed stream); on every error path the body is
// drained bounded and closed. Errors never include the response body or
// resolved secrets.
func doStreaming(ctx context.Context, hc *http.Client, endpointURL string, body []byte, apiKey string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openresponsescompat: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := hc.Do(req)
	if err != nil {
		safeErr := sanitizeHTTPTransportError(err)
		if transporterr.IsRetryable(safeErr) {
			return nil, lipapi.RecoverablePreOutputError(fmt.Errorf("openresponsescompat: create request: %w", safeErr))
		}
		return nil, fmt.Errorf("openresponsescompat: create request failed: %w", safeErr)
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = readHTTPBodyLimited(resp.Body, maxErrorSnippetBytes)
		return nil, classifyHTTPStatus(resp.StatusCode)
	}
	if !isEventStreamContentType(resp.Header.Get("Content-Type")) {
		_, _ = readHTTPBodyLimited(resp.Body, maxErrorSnippetBytes)
		// A wrong content-type is a protocol failure before any output was
		// committed, so core may fail over to another candidate.
		return nil, lipapi.RecoverablePreOutputError(fmt.Errorf("%s: %w: unexpected content-type %q for SSE response", ID, ErrMalformedResponse, resp.Header.Get("Content-Type")))
	}
	return resp, nil
}

func isEventStreamContentType(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return strings.EqualFold(mt, "text/event-stream")
}
