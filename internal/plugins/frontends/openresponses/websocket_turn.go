package openresponses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	proto "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/protocols/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

// SessionRunnerConfig supplies the Task 6.2/6.3 turn-execution dependencies. The
// runner reuses the exact same injected executor and protocol state machine as
// the HTTP/SSE handler; connection-local continuation state lives on the session.
type SessionRunnerConfig struct {
	// Executor is the canonical event-stream executor shared with the HTTP
	// frontend. A nil executor makes every accepted turn fail with a classified
	// operation_not_implemented error without killing the connection.
	Executor ExecutorView
	// DefaultRouteSelector and RoutePrefixes mirror the HTTP decode options so a
	// WebSocket turn resolves its canonical route the same way a POST does.
	DefaultRouteSelector string
	RoutePrefixes        []string
	// MaxMessageBytes bounds one turn envelope during decode. Zero uses the
	// protocol request limit, which also bounds the transport read.
	MaxMessageBytes int64
	// ProtocolLimits bounds the shared OpenResponses request codec.
	ProtocolLimits proto.Limits
	// ResponseIDSource and ResponseClock are proxy-owned envelope metadata shared
	// with the non-streaming/SSE handlers.
	ResponseIDSource ResponseIDSource
	ResponseClock    ResponseClock
	// MaterializeBounds bound parent-chain reconstruction for a continued turn.
	// Zero uses the contract defaults.
	MaterializeBounds lipcont.Bounds
	// RecorderFactory is the narrow seam for incremental terminal recording into
	// the connection-local store. A nil factory uses the standard core recorder.
	RecorderFactory ContinuationRecorderFactory
}

// SessionRunner implements WSSessionRunner: it processes each client text frame
// as a strict response.create envelope and executes accepted turns sequentially
// on the session pump goroutine. At most one turn is in flight; the transport
// queue is bounded by the session's max_queued_turns, and every write happens
// on the pump goroutine so data writes never race the pinger's control frames.
type SessionRunner struct {
	cfg SessionRunnerConfig
}

// NewSessionRunner constructs a turn runner for one frontend instance. The
// runner is stateless across sessions and safe to share between connections.
func NewSessionRunner(cfg SessionRunnerConfig) *SessionRunner {
	return &SessionRunner{cfg: cfg}
}

var _ WSSessionRunner = (*SessionRunner)(nil)

// wsTurnError is a classified WebSocket turn error. Turn-level errors are
// written as a JSON error envelope and keep the connection alive; only transport
// failures (write errors, context cancellation) terminate the session.
type wsTurnError struct {
	status  int
	code    string
	message string
	param   string
}

func (t *wsTurnError) envelope() wsWireErrorEnvelope {
	return wsWireErrorEnvelope{
		Type:   "error",
		Status: t.status,
		Error: wsWireErrorBody{
			Code:    t.code,
			Message: t.message,
			Param:   t.param,
		},
	}
}

// HandleMessage processes one client text frame. It returns nil for handled
// turns (including classified turn errors) and returns an error only for fatal
// conditions that must terminate the session: a dead peer or a canceled session.
func (r *SessionRunner) HandleMessage(ctx context.Context, s *WSSession, data []byte) error {
	decoded, terr := decodeWSCreateEnvelope(data, wsTurnDecodeOptions{
		DefaultRouteSelector: r.cfg.DefaultRouteSelector,
		RoutePrefixes:        r.cfg.RoutePrefixes,
		MaxMessageBytes:      r.cfg.MaxMessageBytes,
		Limits:               r.cfg.ProtocolLimits,
	})
	if terr != nil {
		// A classified 4xx/5xx turn failure evicts the referenced local parent so
		// the same ID cannot be reused after a failed continuation attempt.
		evictWSParentIfReferenced(s, decoded)
		return s.WriteJSON(terr.envelope())
	}
	if isNilExecutor(r.cfg.Executor) {
		// A disabled executor is still a classified turn failure. Evict a
		// referenced local parent before writing the envelope so a failed
		// continuation cannot be replayed.
		evictWSParentIfReferenced(s, decoded)
		return s.WriteJSON((&wsTurnError{
			status:  http.StatusNotImplemented,
			code:    "operation_not_implemented",
			message: "OpenResponses responses is not enabled",
		}).envelope())
	}
	return r.executeTurn(ctx, s, decoded)
}

// extractWSPreviousResponseID is retained as a small parser utility for fuzz and
// compatibility tests. Runtime rejection handling uses decodedWSTurn instead, so
// it does not parse the raw frame a second time.
func extractWSPreviousResponseID(data []byte) string {
	fields, err := parseWSTurnObject(data)
	if err != nil {
		return ""
	}
	raw, ok := fields["previous_response_id"]
	if !ok || !isPresentNonNullJSON(raw) {
		return ""
	}
	var previous string
	if err := json.Unmarshal(raw, &previous); err != nil {
		return ""
	}
	return strings.TrimSpace(previous)
}

// evictWSParentIfReferenced deletes the local parent referenced by a rejected
// turn. A malformed frame cannot carry a usable ID; a classified rejection that
// did reference one must evict it.
func evictWSParentIfReferenced(s *WSSession, decoded *decodedWSTurn) {
	if decoded == nil || decoded.previousResponseID == "" {
		return
	}
	evictWSContinuationParent(s.LocalStore(), s.ContinuationScope(), lipcont.ResponseID(decoded.previousResponseID))
}

// isNilExecutor reports whether an ExecutorView holds no callable implementation,
// including an interface wrapping a typed nil pointer.
func isNilExecutor(e ExecutorView) bool {
	if e == nil {
		return true
	}
	rv := reflect.ValueOf(e)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
		return rv.IsNil()
	}
	return false
}

// watchPeerClosed cancels the derived context when the session observes a fatal
// transport error (peer close or failed control write). The watcher goroutine
// is owned: it exits when either the peer closes or the derived context is
// canceled, so stopPeerClosed (or the deferred cancel) always joins it.
func watchPeerClosed(ctx context.Context, peerClosed <-chan struct{}, cancel context.CancelFunc) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-peerClosed:
			cancel()
		case <-ctx.Done():
		}
	}()
	return func() {
		select {
		case <-done:
		default:
			cancel()
			<-done
		}
	}
}

// executeTurn runs one accepted turn to its terminal event and writes the same
// protocol StreamEvent JSON objects the SSE transport writes, without SSE
// framing and without [DONE]. Cancellation propagates to the executor when the
// peer closes or the session context is canceled.
//
// Connection-local continuation (Task 6.3) resolves previous_response_id from
// the session's local store before any executor work, records the completed
// turn under its proxy response ID, and evicts a referenced parent only after a
// classified 4xx/5xx-equivalent failure. Disconnect, cancellation, and transport
// failures never evict a parent.
func (r *SessionRunner) executeTurn(ctx context.Context, s *WSSession, decoded *decodedWSTurn) error {
	turnCtx, cancel := context.WithCancel(ctx)
	stopPeerWatch := watchPeerClosed(turnCtx, s.PeerClosed(), cancel)
	defer func() {
		stopPeerWatch()
		cancel()
	}()

	localStore := s.LocalStore()
	scope := s.ContinuationScope()
	parentID := lipcont.ResponseID(decoded.previousResponseID)

	// Compaction output is a new chain base: it must never be appended to a
	// prior parent. Combining a compaction item with previous_response_id is a
	// classified continuation failure.
	if decoded.previousResponseID != "" && hasCompactionInput(decoded.call) {
		return r.failClassified(s, localStore, scope, parentID, &wsTurnError{
			status:  http.StatusBadRequest,
			code:    "invalid_request",
			message: "compaction input cannot reference a previous response; start a new chain",
			param:   "previous_response_id",
		})
	}

	execCall := *decoded.call
	var parentRec lipcont.ContinuationRecord
	if decoded.previousResponseID != "" {
		if localStore == nil {
			return r.failClassified(s, localStore, scope, parentID, previousNotFoundTurnError())
		}
		rec, err := lipcont.Lookup(turnCtx, localStore, scope, parentID)
		if err != nil {
			if turnCtx.Err() != nil {
				return turnCtx.Err()
			}
			if errors.Is(err, lipcont.ErrStorageFailure) {
				return r.failClassified(s, localStore, scope, parentID, storageUnavailableTurnError())
			}
			return r.failClassified(s, localStore, scope, parentID, previousNotFoundTurnError())
		}
		materialized, _, err := lipcont.MaterializeCall(turnCtx, lipcont.MaterializeInput{
			Store:    localStore,
			Scope:    scope,
			StartID:  parentID,
			NewInput: decoded.call.Items,
			Bounds:   r.materializeBounds(),
		}, *decoded.call)
		if err != nil {
			if turnCtx.Err() != nil {
				return turnCtx.Err()
			}
			if errors.Is(err, lipcont.ErrStorageFailure) {
				return r.failClassified(s, localStore, scope, parentID, storageUnavailableTurnError())
			}
			if errors.Is(err, lipcont.ErrChainDepthExceeded) {
				return r.failClassified(s, localStore, scope, parentID, continuationBoundsTurnError("Continuation chain depth exceeded"))
			}
			if errors.Is(err, lipcont.ErrStorageLimitExceeded) || errors.Is(err, lipcont.ErrMaterializedSizeExceeded) || errors.Is(err, lipcont.ErrMaterializedItemsExceeded) {
				return r.failClassified(s, localStore, scope, parentID, continuationBoundsTurnError("Continuation storage limit exceeded"))
			}
			return r.failClassified(s, localStore, scope, parentID, previousNotFoundTurnError())
		}
		execCall = materialized
		parentRec = rec
		if decoded.model == "" {
			decoded.model = rec.Lineage.Model
		}
		if execCall.Route.Selector == "" {
			execCall.Route.Selector = rec.Lineage.RouteSelector
			if execCall.Route.Selector == "" {
				execCall.Route.Selector = rec.Lineage.Model
			}
		}
	}

	responseID := ""
	if r.cfg.ResponseIDSource != nil {
		responseID = r.cfg.ResponseIDSource.NewResponseID()
	} else {
		responseID = (systemResponseIDSource{}).NewResponseID()
	}
	clock := r.cfg.ResponseClock
	if clock == nil {
		clock = systemResponseClock{}
	}
	storeFalse := false
	sm := proto.NewStateMachine(proto.EnvelopeMetadata{
		ResponseID:         responseID,
		PreviousResponseID: parentID.String(),
		CreatedAt:          clock.Now(),
		Model:              decoded.model,
		Store:              &storeFalse,
	}, execCall.Options, effectiveProtocolLimits(r.cfg.ProtocolLimits))

	stream, err := r.cfg.Executor.Execute(turnCtx, &execCall)
	if err != nil {
		if turnCtx.Err() != nil {
			return turnCtx.Err()
		}
		return r.failClassified(s, localStore, scope, parentID, backendErrorTurn())
	}
	if stream == nil {
		return r.failClassified(s, localStore, scope, parentID, backendErrorTurn())
	}
	// Enforce the allowed_tools hard constraint before any WebSocket turn output.
	stream = newAllowedToolsStream(&execCall, stream)
	defer func() { _ = stream.Close() }()

	if localStore != nil {
		observer := r.newLocalRecorder(localStore, scope, lipcont.ResponseID(responseID), parentID, parentRec, &execCall, decoded)
		if observer != nil {
			stream = &observedEventStream{EventStream: stream, observer: observer}
		}
	}

	committed := false
	for range maxStreamingEvents {
		ev, err := stream.Recv(turnCtx)
		if err != nil {
			if turnCtx.Err() != nil {
				return turnCtx.Err()
			}
			if errors.Is(err, io.EOF) {
				if committed {
					return r.failTerminal(s, localStore, scope, parentID, sm)
				}
				return r.failClassified(s, localStore, scope, parentID, backendErrorTurn())
			}
			if committed {
				return r.failTerminal(s, localStore, scope, parentID, sm)
			}
			return r.failClassified(s, localStore, scope, parentID, backendErrorTurn())
		}
		if err := lipapi.ValidateEventEnvelope(&ev); err != nil {
			if committed {
				return r.failTerminal(s, localStore, scope, parentID, sm)
			}
			return r.failClassified(s, localStore, scope, parentID, backendErrorTurn())
		}
		if ev.Kind == lipapi.EventError {
			// Preserve the semantic code while applying the same safe-message
			// policy as HTTP/SSE to post-output terminal events.
			if ev.ErrorCode == "" {
				ev.ErrorCode = "backend_error"
			}
			ev.ErrorMessage = safeCanonicalErrorMessage(ev.ErrorMessage)
		}
		events, processErr := sm.ProcessCanonicalEvent(ev)
		if processErr != nil {
			if committed {
				return r.failTerminal(s, localStore, scope, parentID, sm)
			}
			return r.failClassified(s, localStore, scope, parentID, wsTurnErrorFromProtoErr(processErr))
		}
		if err := r.writeStreamEvents(s, events); err != nil {
			return err
		}
		committed = true
		if sm.State() == proto.StateTerminal {
			// A failed terminal is a classified application failure and evicts a
			// referenced local parent exactly like a pre-output classified error.
			if sm.Status() == "failed" {
				evictWSContinuationParent(localStore, scope, parentID)
			}
			return nil
		}
	}
	if committed {
		return r.failTerminal(s, localStore, scope, parentID, sm)
	}
	return r.failClassified(s, localStore, scope, parentID, backendErrorTurn())
}

// materializeBounds returns the configured continuation traversal bounds, falling
// back to the contract defaults when the runner carries none.
func (r *SessionRunner) materializeBounds() lipcont.Bounds {
	if r.cfg.MaterializeBounds.MaxChainDepth > 0 || r.cfg.MaterializeBounds.MaxMaterializedBytes > 0 || r.cfg.MaterializeBounds.MaxMaterializedItems > 0 {
		return r.cfg.MaterializeBounds
	}
	return lipcont.DefaultBounds()
}

// newLocalRecorder builds the best-effort observer that persists a completed
// store:false turn under its proxy response ID in the connection-local store.
func (r *SessionRunner) newLocalRecorder(store lipcont.Store, scope lipcont.Scope, responseID lipcont.ResponseID, parentID lipcont.ResponseID, parentRec lipcont.ContinuationRecord, execCall *lipapi.Call, decoded *decodedWSTurn) lipcont.StreamObserver {
	if store == nil || responseID.IsZero() {
		return nil
	}
	recorderFactory := ContinuationRecorderFactory(connectionContinuationRecorderFactory{})
	if r.cfg.RecorderFactory != nil {
		recorderFactory = r.cfg.RecorderFactory
	}
	return recorderFactory.NewRecorder(store, lipcont.ContinuationRecord{
		ID:           responseID,
		Scope:        scope,
		PreviousID:   parentID,
		ProfileID:    DefaultProfile,
		Lineage:      lipcont.Lineage{ProfileID: DefaultProfile, Model: decoded.model, RouteSelector: execCall.Route.Selector},
		InputItems:   lipcont.CloneItems(decoded.call.Items),
		Requirements: lipapi.DeriveProtocolRequirements(*execCall),
		Policy:       lipcont.StoragePolicy{Mode: lipcont.PersistenceConnection, Limits: lipcont.DefaultStorageLimits()},
		ChainDepth:   parentRec.ChainDepth + 1,
	})
}

// hasCompactionInput reports whether the turn's ordered input carries a
// compaction item, i.e. a compacted window intended as a new chain base.
func hasCompactionInput(call *lipapi.Call) bool {
	if call == nil {
		return false
	}
	for i := range call.Items {
		if call.Items[i].Kind == lipapi.ItemKindCompaction && call.Items[i].Compaction != nil {
			return true
		}
	}
	return false
}

// failClassified writes a classified 4xx/5xx turn error and, when the turn
// referenced a local parent, evicts that parent. This is the only eviction path
// alongside failTerminal; transport/cancellation failures never reach it.
func (r *SessionRunner) failClassified(s *WSSession, store lipcont.Store, scope lipcont.Scope, parentID lipcont.ResponseID, terr *wsTurnError) error {
	evictWSContinuationParent(store, scope, parentID)
	return r.writeTurnClassifiedError(s, terr)
}

// failTerminal drives a post-output failure through the shared state machine and
// evicts a referenced parent exactly like a classified pre-output failure.
func (r *SessionRunner) failTerminal(s *WSSession, store lipcont.Store, scope lipcont.Scope, parentID lipcont.ResponseID, sm *proto.StateMachine) error {
	evictWSContinuationParent(store, scope, parentID)
	return r.emitTerminalFailure(s, sm)
}

func previousNotFoundTurnError() *wsTurnError {
	return &wsTurnError{
		status:  http.StatusBadRequest,
		code:    "previous_response_not_found",
		message: "Previous response was not found",
		param:   "previous_response_id",
	}
}

func storageUnavailableTurnError() *wsTurnError {
	return &wsTurnError{
		status:  http.StatusInternalServerError,
		code:    "storage_error",
		message: "Continuation storage is unavailable",
	}
}

func backendErrorTurn() *wsTurnError {
	return &wsTurnError{
		status:  http.StatusBadGateway,
		code:    "backend_error",
		message: "Backend execution failed",
	}
}

func continuationBoundsTurnError(message string) *wsTurnError {
	return &wsTurnError{
		status:  http.StatusBadRequest,
		code:    "invalid_request",
		message: message,
	}
}

// writeStreamEvents marshals each protocol StreamEvent into its own plain JSON
// text frame. Data writes stay on the session pump goroutine; the pinger only
// issues control frames, which the WebSocket library allows concurrently.
func (r *SessionRunner) writeStreamEvents(s *WSSession, events []proto.StreamEvent) error {
	for i := range events {
		b, err := json.Marshal(events[i])
		if err != nil {
			return err
		}
		if err := s.WriteText(b); err != nil {
			return err
		}
	}
	return nil
}

// emitTerminalFailure drives a classified failure through the shared state
// machine so the connection receives the same response.failed terminal (and
// response resource) the SSE transport produces after commitment. Terminal
// ownership stays with the state machine: a failure after a terminal writes
// nothing.
func (r *SessionRunner) emitTerminalFailure(s *WSSession, sm *proto.StateMachine) error {
	if sm.State() == proto.StateTerminal {
		return nil
	}
	events, err := sm.ProcessCanonicalEvent(lipapi.Event{
		Kind:         lipapi.EventError,
		ErrorCode:    "backend_error",
		ErrorMessage: "Backend execution failed",
	})
	if err != nil {
		return err
	}
	return r.writeStreamEvents(s, events)
}

func (r *SessionRunner) writeTurnClassifiedError(s *WSSession, terr *wsTurnError) error {
	if terr == nil {
		return nil
	}
	return s.WriteJSON(terr.envelope())
}

// wsTurnDecodeOptions is the auth-free decode configuration for a WebSocket turn.
type wsTurnDecodeOptions struct {
	DefaultRouteSelector string
	RoutePrefixes        []string
	MaxMessageBytes      int64
	Limits               proto.Limits
}

// decodedWSTurn is the canonical outcome of decoding one response.create envelope.
type decodedWSTurn struct {
	call               *lipapi.Call
	model              string
	previousResponseID string
}

// decodeWSCreateEnvelope parses a response.create text frame using the same
// canonical request semantics as the HTTP decode path:
//   - the frame must be a strict JSON object with type "response.create";
//   - HTTP-only transport fields (stream, stream_options, background) are rejected;
//   - previous_response_id is accepted for connection-local continuation and may
//     substitute for an absent model (the parent lineage resolves the route);
//   - store:true is rejected (unsupported_parameter) because WebSocket turns are
//     connection-local only; store:false and an omitted store are accepted;
//   - the remaining body decodes through the protocol codec shared with HTTP.
func decodeWSCreateEnvelope(data []byte, opts wsTurnDecodeOptions) (decoded *decodedWSTurn, terr *wsTurnError) {
	var previousResponseID string
	defer func() {
		if terr != nil && decoded == nil && previousResponseID != "" {
			decoded = &decodedWSTurn{previousResponseID: previousResponseID}
		}
	}()

	maxBytes := opts.MaxMessageBytes
	if maxBytes <= 0 {
		maxBytes = proto.MaxRequestBytes
	}

	fields, err := parseWSTurnObject(data)
	if err != nil {
		return nil, &wsTurnError{
			status:  http.StatusBadRequest,
			code:    "invalid_request",
			message: "turn must be a valid JSON object",
		}
	}
	// Capture the parent reference immediately after the single strict parse so
	// later validation failures can evict it without reparsing the raw frame.
	if rawPrevious, ok := fields["previous_response_id"]; ok && isPresentNonNullJSON(rawPrevious) {
		var previous string
		if json.Unmarshal(rawPrevious, &previous) == nil {
			previousResponseID = strings.TrimSpace(previous)
		}
	}
	if int64(len(data)) > maxBytes {
		return nil, &wsTurnError{
			status:  http.StatusRequestEntityTooLarge,
			code:    "limit_exceeded",
			message: "turn payload exceeds the message size limit",
			param:   "request_size",
		}
	}

	rawType, ok := fields["type"]
	if !ok {
		return nil, &wsTurnError{
			status:  http.StatusBadRequest,
			code:    "invalid_message_type",
			message: "each turn must begin with a response.create envelope",
			param:   "type",
		}
	}
	var turnType string
	if err := json.Unmarshal(rawType, &turnType); err != nil || turnType != "response.create" {
		return nil, &wsTurnError{
			status:  http.StatusBadRequest,
			code:    "invalid_message_type",
			message: "unsupported WebSocket message type",
			param:   "type",
		}
	}

	for _, field := range []string{"stream", "stream_options", "background"} {
		if _, ok := fields[field]; ok {
			return nil, &wsTurnError{
				status:  http.StatusBadRequest,
				code:    "field_not_allowed",
				message: fmt.Sprintf("field %q is not allowed on WebSocket requests", field),
				param:   field,
			}
		}
	}

	if raw, ok := fields["store"]; ok && isPresentNonNullJSON(raw) {
		var store bool
		if err := json.Unmarshal(raw, &store); err != nil {
			return nil, &wsTurnError{
				status:  http.StatusBadRequest,
				code:    "invalid_request",
				message: "store must be a boolean",
				param:   "store",
			}
		}
		if store {
			return nil, &wsTurnError{
				status:  http.StatusBadRequest,
				code:    "unsupported_parameter",
				message: "store is not supported on WebSocket turns",
				param:   "store",
			}
		}
	}

	delete(fields, "type")
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, &wsTurnError{
			status:  http.StatusBadRequest,
			code:    "invalid_request",
			message: "invalid turn payload",
		}
	}

	limits := opts.Limits
	if limits == (proto.Limits{}) {
		limits = proto.DefaultLimits()
	}
	wireParam, canonicalCall, err := proto.DecodeRequest(body, limits)
	if err != nil {
		return nil, wsTurnErrorFromProtoErr(err)
	}

	// Create admission: official non-null controls the canonical call cannot
	// represent must fail this turn rather than reach execution while ignored.
	if err := rejectUnsupportedControls(wireParam, createUnsupportedControls); err != nil {
		return nil, wsTurnErrorFromProtoErr(err)
	}

	modelStr := ""
	if wireParam.Model != nil {
		modelStr = strings.TrimSpace(*wireParam.Model)
	}
	prevID := ""
	if wireParam.PreviousResponseID != nil {
		prevID = strings.TrimSpace(*wireParam.PreviousResponseID)
	}
	previousResponseID = prevID
	if modelStr == "" && prevID == "" {
		return nil, &wsTurnError{
			status:  http.StatusBadRequest,
			code:    "invalid_request",
			message: "model is required when previous_response_id is absent",
			param:   "model",
		}
	}
	clientModel := modelStr
	resolvedModel, routeErr := resolveRouteSelector(modelStr, "", opts.RoutePrefixes, opts.DefaultRouteSelector)
	if routeErr != nil {
		return nil, &wsTurnError{
			status:  http.StatusBadRequest,
			code:    "invalid_request",
			message: routeErr.Error(),
			param:   "model",
		}
	}
	modelStr = resolvedModel
	if modelStr == "" && prevID == "" {
		return nil, &wsTurnError{
			status:  http.StatusBadRequest,
			code:    "invalid_request",
			message: "model could not be resolved",
			param:   "model",
		}
	}

	canonicalCall.Route = lipapi.RouteIntent{Selector: modelStr}
	canonicalCall.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenResponsesCreate,
		DeliveryMode:  lipapi.DeliveryModeStreaming,
		TransportMode: lipapi.TransportModeStreaming,
	}
	if _, err := decodeMetadata(wireParam.Metadata); err != nil {
		return nil, wsTurnErrorFromProtoErr(fmt.Errorf("%w: metadata: %w", proto.ErrDecodeFailed, err))
	}
	if err := canonicalCall.Validate(); err != nil {
		return nil, wsTurnErrorFromProtoErr(fmt.Errorf("%w: canonical request: %w", proto.ErrDecodeFailed, err))
	}

	return &decodedWSTurn{call: &canonicalCall, model: clientModel, previousResponseID: prevID}, nil
}

// parseWSTurnObject strict-parses a single JSON object and rejects duplicate
// top-level keys and trailing data, matching the HTTP decode path's strictness.
func parseWSTurnObject(data []byte) (map[string]json.RawMessage, error) {
	if len(data) == 0 {
		return nil, errors.New("empty turn payload")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("turn must be an object")
	}
	fields := make(map[string]json.RawMessage)
	for dec.More() {
		keyToken, err := dec.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return nil, errors.New("invalid turn field")
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate turn field %q", key)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("invalid turn field %q", key)
		}
		fields[key] = raw
	}
	if _, err := dec.Token(); err != nil {
		return nil, errors.New("invalid turn object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing turn data")
	}
	return fields, nil
}

// wsTurnErrorFromProtoErr reuses the protocol error classifier so WebSocket turn
// failures use the same stable classifications as HTTP/SSE.
func wsTurnErrorFromProtoErr(err error) *wsTurnError {
	status, env, _ := proto.MapErrorToWire(err)
	return &wsTurnError{
		status:  status,
		code:    env.Error.Code,
		message: env.Error.Message,
		param:   env.Error.Param,
	}
}

// isPresentNonNullJSON reports whether a raw JSON value is present and not the
// literal null token.
func isPresentNonNullJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}
