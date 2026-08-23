// Package jsonbody owns HTTP-adapter JSON body bounds and decode policy:
// bounded body read, shape preflight against the request-envelope profile
// (which also enforces exactly one JSON document), then typed decode.
package jsonbody

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonshape"
)

// ErrTooLarge reports a request body that exceeds Policy.MaxBytes.
var ErrTooLarge = errors.New("jsonbody: request body too large")

// ErrEmpty reports an empty body when Policy.AllowEmpty is false.
var ErrEmpty = errors.New("jsonbody: empty request body")

// Policy controls one request JSON decode.
type Policy struct {
	MaxBytes   int64
	AllowEmpty bool
}

// Decode reads one bounded request body, shape-preflights it against the
// request-envelope profile capped to MaxBytes (rejecting trailing or multiple
// documents), and decodes it into dest using the request context. HTTP status
// and wire error mapping remain the caller's responsibility.
func Decode(w http.ResponseWriter, r *http.Request, dest any, policy Policy) error {
	if r == nil {
		return errors.New("jsonbody: nil request")
	}
	return decode(w, r, dest, policy, r.Context())
}

// DecodeIgnoringCancellation preserves adapters that decode before forwarding
// an already-canceled request to their domain service. Body and shape errors
// are still returned normally.
func DecodeIgnoringCancellation(w http.ResponseWriter, r *http.Request, dest any, policy Policy) error {
	return decode(w, r, dest, policy, context.Background())
}

func decode(w http.ResponseWriter, r *http.Request, dest any, policy Policy, ctx context.Context) error {
	if r == nil {
		return errors.New("jsonbody: nil request")
	}
	if dest == nil {
		return errors.New("jsonbody: nil destination")
	}
	if policy.MaxBytes <= 0 {
		return errors.New("jsonbody: max bytes must be positive")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.ContentLength > policy.MaxBytes {
		return ErrTooLarge
	}

	body := r.Body
	if body == nil {
		body = http.NoBody
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, body, policy.MaxBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return ErrTooLarge
		}
		// A body whose final read surfaces a wrapped io.EOF (e.g. a connection
		// that died before any bytes) is an empty body for AllowEmpty adapters.
		// io.ReadAll only normalizes a raw io.EOF, so this branch is reachable.
		if policy.AllowEmpty && len(data) == 0 && errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		if policy.AllowEmpty {
			return nil
		}
		return ErrEmpty
	}

	shape := jsonshape.RequestEnvelopeLimits()
	shape.MaxBytes = policy.MaxBytes
	if _, err := jsonshape.PreflightWithContext(ctx, data, shape); err != nil {
		return err
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(dest); err != nil {
		return err
	}
	return nil
}
