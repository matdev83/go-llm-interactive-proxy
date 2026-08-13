// Package frontendpipe provides a shared HTTP create pipeline for wire frontends.
package frontendpipe

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/decodeqos"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/execerr"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/holdalive"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/jsonguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/reqbody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/routeselect"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/streamdebug"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

// PathMatch carries path-derived inputs for decode (e.g. Gemini model/stream flag).
type PathMatch struct {
	Model  string
	Stream bool
	Extra  any
}

// Decoded is the outcome of protocol decode + canonical validation prep.
type Decoded struct {
	Call   *lipapi.Call
	Stream bool
	// RouteSelector is the resolved routing selector used for decode/logging.
	RouteSelector string
}

// DecodeContext supplies request inputs for protocol decode.
type DecodeContext struct {
	Ctx              context.Context
	Body             []byte
	RouteSelector    string
	Headers          http.Header
	Path             PathMatch
	AnthropicVersion string
}

// WireErrors writes protocol-legal JSON (or equivalent) error bodies.
type WireErrors interface {
	WriteBodyTooLarge(w http.ResponseWriter) error
	WriteReadBodyFailed(w http.ResponseWriter) error
	WriteExecutorNotConfigured(w http.ResponseWriter) error
	WritePreflightCanceled(w http.ResponseWriter) error
	WriteInvalidJSON(w http.ResponseWriter) error
	WriteAdmissionReject(w http.ResponseWriter, d decodeqos.Decision) error
	WriteInvalidRequest(w http.ResponseWriter) error
	WriteExecuteError(w http.ResponseWriter, out execerr.Outcome) error
	WriteEncodeFailed(w http.ResponseWriter) error
}

// Config holds shared handler dependencies for the create pipeline.
type Config struct {
	Exec                 lipsdk.ExecutorView
	DefaultRouteSelector string
	RoutePrefixes        routeselect.PrefixSet
	MaxRequestBodyBytes  int64
	Log                  *slog.Logger
	TrafficPorts         traffic.PortBundle
	DecodeAdmission      lipsdk.DecodeAdmission
	PreRequestKeepalive  lipsdk.FrontendKeepaliveConfig
	FrontendID           string
}

// Spec parameterizes one frontend's create path.
type Spec[Opts any] struct {
	Config
	Wire WireErrors
	// MatchPath returns ok=false for 404. When AltServe is non-nil and invoked, the pipeline stops.
	MatchPath func(path string) (pm PathMatch, ok bool)
	AltServe  func(ctx context.Context, w http.ResponseWriter, r *http.Request) bool
	// ResolveRouteSelector runs after body read; pm is zero when MatchPath did not apply.
	ResolveRouteSelector func(r *http.Request, body []byte, pm PathMatch) string
	Decode               func(DecodeContext) (*Decoded, error)
	BuildEncodeOpts      func(call *lipapi.Call, stream bool) Opts
	WriteStream          func(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, opts Opts) error
	WriteNonStream       func(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, opts Opts) error
	// RouteFromBodyModel defaults route selector from JSON model field when header absent.
	RouteFromBodyModel bool
}

func (c Config) maxBodyLimit() int64 {
	if c.MaxRequestBodyBytes > 0 {
		return c.MaxRequestBodyBytes
	}
	return reqbody.DefaultMaxBytes
}

func (c Config) logWriteJSONErr(ctx context.Context, msg string, werr error) {
	if c.Log == nil || werr == nil {
		return
	}
	diag.LogError(ctx, c.Log, msg, diag.AttrOpts{}, werr)
}

func (c Config) execute(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, stream bool) (lipapi.EventStream, error) {
	if !stream {
		return c.Exec.Execute(ctx, call)
	}
	return holdalive.Wait(ctx, w, holdalive.Config{
		Enabled:  c.PreRequestKeepalive.Enabled,
		Interval: c.PreRequestKeepalive.Interval,
	}, func(ctx context.Context) (lipapi.EventStream, error) {
		return c.Exec.Execute(ctx, call)
	})
}

// ServeHTTP runs the shared decode → execute → encode pipeline.
func ServeHTTP[Opts any](spec *Spec[Opts], w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if spec.AltServe != nil && spec.AltServe(ctx, w, r) {
		return
	}
	pm, ok := spec.MatchPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	limits := jsonguard.Limits{MaxBytes: spec.maxBodyLimit()}
	body, err := reqbody.ReadAll(w, r, limits.MaxBytes)
	if err != nil {
		if reqbody.TooLarge(err) {
			spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteBodyTooLarge(w))
			return
		}
		spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteReadBodyFailed(w))
		return
	}
	ct := strings.TrimSpace(r.Header.Get("Content-Type"))
	if ct == "" {
		ct = "application/octet-stream"
	}
	if spec.Exec == nil {
		spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteExecutorNotConfigured(w))
		return
	}

	sel := strings.TrimSpace(r.Header.Get(routeselect.HeaderRouteSelector))
	if spec.ResolveRouteSelector != nil {
		if v := strings.TrimSpace(spec.ResolveRouteSelector(r, body, pm)); v != "" {
			sel = v
		}
	}
	if _, err := jsonguard.PreflightWithContext(ctx, body, limits); err != nil {
		if jsonguard.Classify(err) == jsonguard.KindCanceled {
			spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WritePreflightCanceled(w))
			return
		}
		spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteInvalidJSON(w))
		return
	}
	releaseDecode, ok, err := decodeqos.TryAdmit(ctx, spec.DecodeAdmission, int64(len(body)))
	if d := decodeqos.Decide(ok, err); d.Status != 0 {
		if d.RetryAfter {
			w.Header().Set("Retry-After", decodeqos.RetryAfterSeconds)
		}
		spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteAdmissionReject(w, d))
		return
	}
	var decoded *Decoded
	err = decodeqos.Guard(releaseDecode, func() error {
		if sel == "" && spec.RouteFromBodyModel {
			sel = spec.RoutePrefixes.FromModelOrDefault(body, spec.DefaultRouteSelector)
		}
		dctx := DecodeContext{
			Ctx:              ctx,
			Body:             body,
			RouteSelector:    sel,
			Headers:          r.Header,
			Path:             pm,
			AnthropicVersion: strings.TrimSpace(r.Header.Get("anthropic-version")),
		}
		var derr error
		decoded, derr = spec.Decode(dctx)
		return derr
	})
	if err != nil {
		log := diag.LoggerOrDefault(spec.Log)
		diag.LogError(ctx, log, "decode request failed", diag.AttrOpts{}, err, slog.String("detail", diag.TruncErrDetail(err, 512)))
		streamdebug.LogDecodeFailure(ctx, log, spec.FrontendID, body, err)
		spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteInvalidJSON(w))
		return
	}
	call := decoded.Call
	if err := call.Validate(); err != nil {
		if spec.Log != nil {
			diag.LogError(ctx, spec.Log, "validate call failed", diag.AttrOpts{CallID: call.ID}, err, slog.String("detail", diag.TruncErrDetail(err, 512)))
		}
		spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteInvalidRequest(w))
		return
	}

	traceID := diag.StableCallID(call)
	ctx = diag.EnsureCallDiag(ctx, traceID, strings.TrimSpace(call.Session.ALegID))
	spec.TrafficPorts.Emit(ctx, traffic.LegCTP, traffic.CaptureMeta{
		TraceID:   traceID,
		SessionID: call.Session.CorrelationID(),
	}, "http", ct, body)

	stream := decoded.Stream
	if pm.Model != "" {
		stream = pm.Stream
	}

	streamdebug.LogCall(ctx, spec.Log, spec.FrontendID, call, stream, len(body), decoded.RouteSelector)
	executeStart := time.Now()
	es, err := spec.execute(ctx, w, call, stream)
	if err != nil {
		out := execerr.ClassifyExecute(err)
		if out.Kind == execerr.KindInternalError && spec.Log != nil && out.Err != nil {
			diag.LogError(ctx, spec.Log, "execute failed", diag.AttrOpts{CallID: call.ID}, out.Err)
		}
		spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteExecuteError(w, out))
		return
	}
	streamdebug.LogExecuteOpened(ctx, spec.Log, spec.FrontendID, call, executeStart)

	ctx = diag.EnsureCallDiag(ctx, traceID, call.Session.ALegID)
	es = streamdebug.Wrap(ctx, spec.Log, spec.FrontendID, call, es, executeStart)

	opts := spec.BuildEncodeOpts(call, stream)
	if stream {
		if err := spec.WriteStream(ctx, w, call, es, opts); err != nil {
			diag.LogError(ctx, spec.Log, "stream encode failed", diag.AttrOpts{CallID: call.ID}, err)
			return
		}
		return
	}
	if err := spec.WriteNonStream(ctx, w, call, es, opts); err != nil {
		diag.LogError(ctx, spec.Log, "non-stream encode failed", diag.AttrOpts{CallID: call.ID}, err)
		spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteEncodeFailed(w))
	}
}
