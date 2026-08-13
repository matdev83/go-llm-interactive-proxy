package frontendpipe_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/decodeqos"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/execerr"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type hookWire struct {
	last string
}

func (w *hookWire) write(rw http.ResponseWriter, status int, msg string) error {
	w.last = msg
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	return json.NewEncoder(rw).Encode(map[string]string{"error": msg})
}

func (w *hookWire) WriteBodyTooLarge(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusRequestEntityTooLarge, "too large")
}
func (w *hookWire) WriteReadBodyFailed(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusBadRequest, "read failed")
}
func (w *hookWire) WriteExecutorNotConfigured(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusInternalServerError, "no executor")
}
func (w *hookWire) WritePreflightCanceled(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusServiceUnavailable, "canceled")
}
func (w *hookWire) WriteInvalidJSON(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusBadRequest, "invalid json")
}
func (w *hookWire) WriteAdmissionReject(rw http.ResponseWriter, d decodeqos.Decision) error {
	return w.write(rw, d.Status, d.Message)
}
func (w *hookWire) WriteInvalidRequest(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusBadRequest, "invalid request")
}
func (w *hookWire) WriteExecuteError(rw http.ResponseWriter, out execerr.Outcome) error {
	return w.write(rw, out.Status, out.Message)
}
func (w *hookWire) WriteEncodeFailed(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusInternalServerError, "encode failed")
}
func (w *hookWire) WriteHookError(rw http.ResponseWriter, err error) error {
	var se *frontendpipe.StatusError
	if errors.As(err, &se) {
		return w.write(rw, se.Status, se.Message)
	}
	return w.write(rw, http.StatusBadRequest, err.Error())
}

type hookExec struct {
	err    error
	called bool
}

func (e *hookExec) Execute(context.Context, *lipapi.Call) (lipapi.EventStream, error) {
	e.called = true
	if e.err != nil {
		return nil, e.err
	}
	return lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventResponseFinished},
	}), nil
}
func (e *hookExec) CancelALeg(context.Context, lipapi.ALegCancelRequest) error { return nil }
func (e *hookExec) WallClock() func() time.Time                                { return nil }

type wrapStream struct {
	lipapi.EventStream
	wrapped bool
}

func validCall() *lipapi.Call {
	return &lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
}

func testSpec(exec *hookExec, wire *hookWire) frontendpipe.Spec[struct{}] {
	return frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec:       exec,
			FrontendID: "hooktest",
		},
		Wire: wire,
		MatchPath: func(string) (frontendpipe.PathMatch, bool) {
			return frontendpipe.PathMatch{}, true
		},
		Decode: func(frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			return &frontendpipe.Decoded{Call: validCall(), Stream: false}, nil
		},
		BuildEncodeOpts: func(*frontendpipe.Decoded) struct{} { return struct{}{} },
		WriteStream: func(context.Context, http.ResponseWriter, *lipapi.Call, lipapi.EventStream, struct{}) error {
			return nil
		},
		WriteNonStream: func(_ context.Context, w http.ResponseWriter, _ *lipapi.Call, es lipapi.EventStream, _ struct{}) error {
			if es != nil {
				_ = es.Close()
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"ok":true}`)
			return nil
		},
	}
}

func TestServeHTTP_AfterDecodeMutatesExtraAndWrapStreamSeesIt(t *testing.T) {
	t.Parallel()
	exec := &hookExec{}
	wire := &hookWire{}
	spec := testSpec(exec, wire)
	var afterDecoded, wrapSaw string
	spec.AfterDecode = func(_ context.Context, decoded *frontendpipe.Decoded) error {
		decoded.Extra = "reserved"
		afterDecoded = "reserved"
		return nil
	}
	spec.WrapStream = func(_ context.Context, decoded *frontendpipe.Decoded, inner lipapi.EventStream) (lipapi.EventStream, error) {
		wrapSaw, _ = decoded.Extra.(string)
		return &wrapStream{EventStream: inner, wrapped: true}, nil
	}
	var wroteWrapped bool
	spec.WriteNonStream = func(_ context.Context, w http.ResponseWriter, _ *lipapi.Call, es lipapi.EventStream, _ struct{}) error {
		ws, ok := es.(*wrapStream)
		wroteWrapped = ok && ws.wrapped
		if es != nil {
			_ = es.Close()
		}
		w.WriteHeader(http.StatusOK)
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{"n":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if afterDecoded != "reserved" || wrapSaw != "reserved" {
		t.Fatalf("extra not forwarded: after=%q wrap=%q", afterDecoded, wrapSaw)
	}
	if !wroteWrapped {
		t.Fatal("WriteNonStream did not receive wrapped stream")
	}
	if !exec.called {
		t.Fatal("executor was not called")
	}
}

func TestServeHTTP_AfterDecodeStatusErrorUsesHookWriter(t *testing.T) {
	t.Parallel()
	exec := &hookExec{}
	wire := &hookWire{}
	spec := testSpec(exec, wire)
	spec.AfterDecode = func(context.Context, *frontendpipe.Decoded) error {
		return &frontendpipe.StatusError{
			Status:  http.StatusBadRequest,
			Type:    "invalid_request_error",
			Code:    "previous_response_not_found",
			Message: "Previous response was not found",
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if wire.last != "Previous response was not found" {
		t.Fatalf("wire last=%q", wire.last)
	}
	if exec.called {
		t.Fatal("executor ran after AfterDecode error")
	}
}

func TestServeHTTP_OnExecuteErrorRunsOnFailure(t *testing.T) {
	t.Parallel()
	exec := &hookExec{err: errors.New("boom")}
	wire := &hookWire{}
	spec := testSpec(exec, wire)
	var cleaned bool
	spec.OnExecuteError = func(context.Context, *frontendpipe.Decoded, error) {
		cleaned = true
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if !cleaned {
		t.Fatal("OnExecuteError was not invoked")
	}
	if rec.Code == http.StatusOK {
		t.Fatal("expected execute error status")
	}
}
