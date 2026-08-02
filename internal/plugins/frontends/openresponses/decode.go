package openresponses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/routeselect"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
	proto "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/protocols/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkauth "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auth"
)

var ErrUnauthorized = errors.New("openresponses: unauthorized")

// DecodeCompactOptions supplies context and dependencies for authenticating and decoding a compact request.
type DecodeCompactOptions struct {
	DefaultRouteSelector string
	RoutePrefixes        []string
	RouteSelector        string
	Headers              http.Header
	Auth                 Authorizer
	MaxBodyBytes         int64
	Limits               proto.Limits
	Method               string
	Path                 string
	RemoteAddr           string
}

// DecodedCompact holds the outcome of authenticated compact request decoding.
type DecodedCompact struct {
	Call          *lipapi.Call
	Requirements  lipapi.ProtocolRequirements
	RouteSelector string
	Model         string
	AuthDecision  sdkauth.Decision
}

// DecodeCreateOptions supplies context and dependencies for authenticating and decoding a create request.
type DecodeCreateOptions struct {
	DefaultRouteSelector string
	RoutePrefixes        []string
	RouteSelector        string
	Headers              http.Header
	Auth                 Authorizer
	MaxBodyBytes         int64
	Limits               proto.Limits
	Method               string
	Path                 string
	RemoteAddr           string
}

// DecodedCreate holds the outcome of authenticated create request decoding.
type DecodedCreate struct {
	Call               *lipapi.Call
	Requirements       lipapi.ProtocolRequirements
	Stream             bool
	RouteSelector      string
	Model              string
	PreviousResponseID string
	Store              bool
	ExplicitStore      *bool
	AuthDecision       sdkauth.Decision
}

// Authorizer performs request-path authentication checks before body parsing or continuation store work.
type Authorizer interface {
	Authenticate(ctx context.Context, meta sdkauth.InboundCallMeta) (sdkauth.Decision, error)
}

// AuthenticateAndDecodeCreate performs authentication checks FIRST, then decodes the request body
// into an item-authoritative canonical [lipapi.Call] with protocol requirements and invocation metadata.
func AuthenticateAndDecodeCreate(ctx context.Context, body []byte, opts DecodeCreateOptions) (*DecodedCreate, error) {
	// 1. Auth check BEFORE body or continuation/store work (Requirement 2.10, 10.8)
	var decision sdkauth.Decision
	if opts.Auth != nil {
		meta := sdkauth.InboundCallMeta{
			Frontend:            ID,
			Method:              opts.Method,
			Path:                opts.Path,
			ClientAddr:          opts.RemoteAddr,
			AuthorizationBearer: extractBearerToken(opts.Headers),
		}
		var err error
		decision, err = opts.Auth.Authenticate(ctx, meta)
		if err != nil {
			return nil, fmt.Errorf("%w: auth failed", ErrUnauthorized)
		}
		if decision.Outcome != sdkauth.OutcomeAllow {
			return nil, fmt.Errorf("%w: %s", ErrUnauthorized, decision.ReasonCode)
		}
	}

	if len(body) == 0 {
		return nil, proto.ErrDecodeFailed
	}
	if opts.MaxBodyBytes > 0 && int64(len(body)) > opts.MaxBodyBytes {
		return nil, fmt.Errorf("%w: request body exceeds configured limit", proto.ErrDecodeFailed)
	}

	// The frontend owns the create-only stream delivery control. Keep it out of
	// the protocol request codec's canonical payload until that codec exposes it.
	decodeBody, stream, err := splitStreamControl(body)
	if err != nil {
		return nil, err
	}

	// 2. Decode wire request using Task 2 protocol codec
	limits := opts.Limits
	if limits == (proto.Limits{}) {
		limits = proto.DefaultLimits()
	}
	wireParam, canonicalCall, err := proto.DecodeRequest(decodeBody, limits)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", proto.ErrDecodeFailed, err)
	}

	// 3. Resolve route selector / model conditional rules
	modelStr := ""
	if wireParam.Model != nil {
		modelStr = strings.TrimSpace(*wireParam.Model)
	}
	prevID := ""
	if wireParam.PreviousResponseID != nil {
		prevID = strings.TrimSpace(*wireParam.PreviousResponseID)
	}
	if modelStr == "" && prevID == "" {
		return nil, fmt.Errorf("%w: model is required when previous_response_id is absent", proto.ErrDecodeFailed)
	}
	clientModel := modelStr
	// A route header is an authoritative transport hint, but it remains subject
	// to the configured prefix allowlist. Inline body selectors use the normal
	// fallback-to-default behavior.
	var routeErr error
	modelStr, routeErr = resolveRouteSelector(modelStr, opts.RouteSelector, opts.RoutePrefixes, opts.DefaultRouteSelector)
	if routeErr != nil {
		return nil, fmt.Errorf("%w: %w", proto.ErrDecodeFailed, routeErr)
	}
	if modelStr == "" && prevID == "" {
		return nil, fmt.Errorf("%w: model could not be resolved", proto.ErrDecodeFailed)
	}

	store := true
	var explicitStore *bool
	if wireParam.Store != nil {
		store = *wireParam.Store
		explicitStore = wireParam.Store
	}

	// 4. Construct complete item-authoritative canonical Call
	canonicalCall.Route = lipapi.RouteIntent{Selector: modelStr}

	// Set Invocation metadata
	deliveryMode := lipapi.DeliveryModeFromClientStream(stream)
	canonicalCall.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenResponsesCreate,
		DeliveryMode:  deliveryMode,
		TransportMode: lipapi.PreferredTransportMode(deliveryMode),
	}
	metadata, err := decodeMetadata(wireParam.Metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: metadata: %w", proto.ErrDecodeFailed, err)
	}
	canonicalCall.Session.Metadata = metadata
	if opts.Headers != nil {
		canonicalCall.Invocation.ClientUserAgent = strings.TrimSpace(opts.Headers.Get("User-Agent"))
		sessionwire.ApplyAuthoritativeHeaders(&canonicalCall.Session, opts.Headers)
	}

	if err := canonicalCall.Validate(); err != nil {
		return nil, fmt.Errorf("%w: canonical request: %w", proto.ErrDecodeFailed, err)
	}

	// Requirements are admission metadata, not a duplicate field on Call. The core
	// derives the same set again when it builds the failover requirement baseline.
	requirements := lipapi.DeriveProtocolRequirements(canonicalCall)

	return &DecodedCreate{
		Call:               &canonicalCall,
		Requirements:       requirements,
		Stream:             stream,
		RouteSelector:      modelStr,
		Model:              clientModel,
		PreviousResponseID: prevID,
		Store:              store,
		ExplicitStore:      explicitStore,
		AuthDecision:       decision,
	}, nil
}

func splitStreamControl(body []byte) ([]byte, bool, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, false, fmt.Errorf("%w: invalid stream control", proto.ErrDecodeFailed)
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil, false, fmt.Errorf("%w: request must be an object", proto.ErrDecodeFailed)
	}
	fields := make(map[string]json.RawMessage)
	for dec.More() {
		keyToken, err := dec.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return nil, false, fmt.Errorf("%w: invalid request field", proto.ErrDecodeFailed)
		}
		if _, exists := fields[key]; exists {
			return nil, false, fmt.Errorf("%w: duplicate request field %q", proto.ErrDecodeFailed, key)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, false, fmt.Errorf("%w: invalid request field %q", proto.ErrDecodeFailed, key)
		}
		fields[key] = raw
	}
	if _, err := dec.Token(); err != nil {
		return nil, false, fmt.Errorf("%w: invalid request object", proto.ErrDecodeFailed)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false, fmt.Errorf("%w: trailing request data", proto.ErrDecodeFailed)
	}
	raw, ok := fields["stream"]
	if !ok {
		return body, false, nil
	}
	var stream bool
	if err := json.Unmarshal(raw, &stream); err != nil {
		return nil, false, fmt.Errorf("%w: stream must be a boolean", proto.ErrDecodeFailed)
	}
	delete(fields, "stream")
	withoutStream, err := json.Marshal(fields)
	if err != nil {
		return nil, false, fmt.Errorf("%w: stream control", proto.ErrDecodeFailed)
	}
	return withoutStream, stream, nil
}

func decodeMetadata(raw json.RawMessage) (map[string]string, error) {
	if len(strings.TrimSpace(string(raw))) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil, nil
	}
	var metadata map[string]string
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, err
	}
	if err := sessionwire.ValidateMetadata(metadata); err != nil {
		return nil, err
	}
	return metadata, nil
}

func resolveRouteSelector(model, explicit string, prefixes []string, defaultRoute string) (string, error) {
	if selector := strings.TrimSpace(explicit); selector != "" {
		if len(prefixes) > 0 && routeselect.NewPrefixSet(prefixes).InlineOrDefault(selector, "") == "" {
			return "", fmt.Errorf("route selector %q is not allowed by configured route prefixes", selector)
		}
		return selector, nil
	}
	if len(prefixes) == 0 {
		return strings.TrimSpace(model), nil
	}
	if strings.TrimSpace(model) == "" {
		return "", nil
	}
	return routeselect.NewPrefixSet(prefixes).InlineOrDefault(model, defaultRoute), nil
}

func extractBearerToken(h http.Header) string {
	if h == nil {
		return ""
	}
	authHdr := strings.TrimSpace(h.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authHdr), "bearer ") {
		return strings.TrimSpace(authHdr[7:])
	}
	return ""
}

// AuthenticateAndDecodeCompact performs authentication checks FIRST, then decodes the request body
// into an item-authoritative canonical [lipapi.Call] with context.compaction operation and protocol requirements.
func AuthenticateAndDecodeCompact(ctx context.Context, body []byte, opts DecodeCompactOptions) (*DecodedCompact, error) {
	// 1. Auth check BEFORE body or continuation/store work (Requirement 2.10, 10.8)
	var decision sdkauth.Decision
	if opts.Auth != nil {
		meta := sdkauth.InboundCallMeta{
			Frontend:            ID,
			Method:              opts.Method,
			Path:                opts.Path,
			ClientAddr:          opts.RemoteAddr,
			AuthorizationBearer: extractBearerToken(opts.Headers),
		}
		var err error
		decision, err = opts.Auth.Authenticate(ctx, meta)
		if err != nil {
			return nil, fmt.Errorf("%w: auth failed", ErrUnauthorized)
		}
		if decision.Outcome != sdkauth.OutcomeAllow {
			return nil, fmt.Errorf("%w: %s", ErrUnauthorized, decision.ReasonCode)
		}
	}

	if len(body) == 0 {
		return nil, proto.ErrDecodeFailed
	}
	if opts.MaxBodyBytes > 0 && int64(len(body)) > opts.MaxBodyBytes {
		return nil, fmt.Errorf("%w: request body exceeds configured limit", proto.ErrDecodeFailed)
	}

	// 2. Reject forbidden compact fields (stream, store, previous_response_id, background)
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawMap); err != nil {
		return nil, fmt.Errorf("%w: %w", proto.ErrDecodeFailed, err)
	}
	for _, forbidden := range []string{"stream", "store", "previous_response_id", "background"} {
		if _, ok := rawMap[forbidden]; ok {
			return nil, fmt.Errorf("%w: %s field is forbidden in compact request", proto.ErrDecodeFailed, forbidden)
		}
	}

	// 3. Decode wire request using Task 2 protocol codec
	limits := opts.Limits
	if limits == (proto.Limits{}) {
		limits = proto.DefaultLimits()
	}
	wireParam, canonicalCall, err := proto.DecodeRequest(body, limits)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", proto.ErrDecodeFailed, err)
	}

	// 4. Validate model is required
	modelStr := ""
	if wireParam.Model != nil {
		modelStr = strings.TrimSpace(*wireParam.Model)
	}
	if modelStr == "" {
		return nil, fmt.Errorf("%w: model is required for compact request", proto.ErrDecodeFailed)
	}
	clientModel := modelStr

	// 5. Resolve route selector, validating explicit transport overrides against
	// the same prefix allowlist as body model selectors.
	var routeErr error
	modelStr, routeErr = resolveRouteSelector(modelStr, opts.RouteSelector, opts.RoutePrefixes, opts.DefaultRouteSelector)
	if routeErr != nil {
		return nil, fmt.Errorf("%w: %w", proto.ErrDecodeFailed, routeErr)
	}
	if modelStr == "" {
		return nil, fmt.Errorf("%w: model could not be resolved", proto.ErrDecodeFailed)
	}

	// 6. Validate input is present and item-authoritative
	if len(canonicalCall.Items) == 0 || !canonicalCall.HasItemAuthority() {
		return nil, fmt.Errorf("%w: input is required for compact request", proto.ErrDecodeFailed)
	}

	// 7. Construct canonical Call for compaction
	canonicalCall.Route = lipapi.RouteIntent{Selector: modelStr}
	canonicalCall.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationContextCompaction,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	if opts.Headers != nil {
		canonicalCall.Invocation.ClientUserAgent = strings.TrimSpace(opts.Headers.Get("User-Agent"))
		sessionwire.ApplyAuthoritativeHeaders(&canonicalCall.Session, opts.Headers)
	}

	if err := canonicalCall.Validate(); err != nil {
		return nil, fmt.Errorf("%w: canonical request: %w", proto.ErrDecodeFailed, err)
	}

	// 8. Derive protocol requirements and ensure CapabilityCompaction is included
	requirements := lipapi.DeriveProtocolRequirements(canonicalCall)
	if !slices.Contains(requirements.Capabilities, lipapi.CapabilityCompaction) {
		requirements.Capabilities = append(requirements.Capabilities, lipapi.CapabilityCompaction)
		requirements = lipapi.NormalizeProtocolRequirements(requirements)
	}

	return &DecodedCompact{
		Call:          &canonicalCall,
		Requirements:  requirements,
		RouteSelector: modelStr,
		Model:         clientModel,
		AuthDecision:  decision,
	}, nil
}
