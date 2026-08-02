package acp

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// httpStatusError carries the upstream HTTP status code for an ACP JSON-RPC
// exchange so the connector can classify pre-output failures. 5xx and 429 are
// recoverable failover candidates; 4xx auth/validation failures are terminal.
type httpStatusError struct {
	Op     string
	Status int
	Detail string
}

func (e *httpStatusError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("acp: %s: HTTP %d: %s", e.Op, e.Status, e.Detail)
	}
	return fmt.Sprintf("acp: %s: HTTP %d", e.Op, e.Status)
}

// classifyPreOutputError classifies a failure that occurred before canonical
// output began. Transport/network and protocol failures are wrapped as
// RecoverablePreOutputError so the core may fail over to another candidate
// (Task 8.5 / FeatureFailover); terminal auth/validation failures (HTTP 4xx and
// JSON-RPC rejections) stay terminal so an invalid request never silently
// retries against another candidate. The "no retry after output" invariant is
// preserved because callers only invoke this before any content event exists.
func classifyPreOutputError(err error) error {
	if err == nil {
		return nil
	}
	var hse *httpStatusError
	if errors.As(err, &hse) {
		if hse.Status >= 500 || hse.Status == http.StatusTooManyRequests {
			return lipapi.RecoverablePreOutputError(err)
		}
		return err
	}
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		return err
	}
	return lipapi.RecoverablePreOutputError(err)
}

// recoverablePreOutput wraps err as a recoverable pre-output failure. It is the
// stream-side counterpart of classifyPreOutputError and is only applied when
// the stream has not yet produced canonical output.
func recoverablePreOutput(err error) error {
	if err == nil {
		return nil
	}
	return lipapi.RecoverablePreOutputError(err)
}
