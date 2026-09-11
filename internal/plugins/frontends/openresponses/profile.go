package openresponses

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonshape"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
	proto "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/protocols/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ProfileID is the static identifier for the certified OpenResponses fast-path profile (Requirements 4, 17).
const ProfileID = "openresponses_v1"

// Profile implements frontendpipe.FrontendProfile for OpenResponses create (POST /openresponses/v1/responses).
type Profile struct{}

// NewProfile returns an initialized OpenResponses frontend profile.
func NewProfile() *Profile {
	return &Profile{}
}

// ProfileID returns the static identifier for this certified profile.
func (p *Profile) ProfileID() string {
	return ProfileID
}

var openResponsesKnownBodyKeys = map[string]bool{
	"model":                  true,
	"input":                  true,
	"instructions":           true,
	"tools":                  true,
	"tool_choice":            true,
	"parallel_tool_calls":    true,
	"temperature":            true,
	"top_p":                  true,
	"max_output_tokens":      true,
	"max_tool_calls":         true,
	"truncation":             true,
	"text":                   true,
	"reasoning":              true,
	"store":                  true,
	"background":             true,
	"previous_response_id":   true,
	"metadata":               true,
	"service_tier":           true,
	"safety_identifier":      true,
	"prompt_cache_key":       true,
	"prompt_cache_retention": true,
	"include":                true,
	"presence_penalty":       true,
	"frequency_penalty":      true,
	"top_logprobs":           true,
	"stream_options":         true,
	"stream":                 true,
}

// CompileProof compiles protocol proof and response seeds from the
// captured request replay under decode admission (Requirements 4, 14, 16, 17).
func (p *Profile) CompileProof(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
	// 1. Path verification: only create path is supported; compact path is declined.
	if !isCreatePath(in.URLPath) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: unsupported url path")
	}
	if isCompactPath(in.URLPath) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: compact path not supported for fast path")
	}

	if in.Source == nil {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: nil replay source")
	}

	rc, err := in.Source.Open()
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: open replay source: %w", err)
	}
	defer rc.Close()

	readBytes, err := io.ReadAll(rc)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: read replay source: %w", err)
	}

	bodyBytes := in.BodyBytes
	if bodyBytes <= 0 {
		bodyBytes = int64(len(readBytes))
	}

	// 2. Scan with jsonshape.Scanner to enforce:
	//    - RejectDuplicateNames (canonical-only if duplicate keys)
	//    - Track exact byte span for "model"
	//    - Detect unknown top-level keys (canonical-only if unknown)
	//    - Detect unsupported control keys (canonical-only)
	tracker := jsonshape.NewTopLevelSpanTracker("model")
	var unknownKey string
	handler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
		if e.TopLevel && e.Type == jsonshape.EventKey {
			if !openResponsesKnownBodyKeys[e.Key] {
				unknownKey = e.Key
			}
		}
		return tracker.OnEvent(e)
	})

	scannerLimits := jsonshape.Limits{
		RejectDuplicateNames: true,
		MaxBytes:             bodyBytes,
	}
	scanCtx := in.Ctx
	if scanCtx == nil {
		scanCtx = ctx
	}
	scanner := jsonshape.NewScanner(
		scanCtx,
		scannerLimits,
		jsonshape.WithEventHandler(handler),
	)

	hasher := sha256.New()
	chunkSize := 32 * 1024
	for offset := 0; offset < len(readBytes); offset += chunkSize {
		end := min(offset+chunkSize, len(readBytes))
		chunk := readBytes[offset:end]
		hasher.Write(chunk)
		if err := scanner.Feed(chunk); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: json scanner feed: %w", err)
		}
	}
	if _, err := scanner.Finish(); err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: json scanner finish: %w", err)
	}

	if unknownKey != "" {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: unknown body key %q", unknownKey)
	}

	// 3. Verify top-level "model" span
	modelSpanRaw, hasModel := tracker.Span("model")
	if !hasModel || modelSpanRaw.Length == 0 {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: model is required")
	}
	modelSpan := largebody.Span{Offset: modelSpanRaw.Offset, Length: modelSpanRaw.Length}
	rewrite, err := largebody.NewModelTokenRewrite(modelSpan)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: model rewrite: %w", err)
	}

	// 4. Requirement 17.4: Explicit store: false gate.
	// store must be explicitly present and strictly equal to boolean false.
	var rawStore struct {
		Store *json.RawMessage `json:"store"`
	}
	if err := json.Unmarshal(readBytes, &rawStore); err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: unmarshal store: %w", err)
	}
	if rawStore.Store == nil {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: store is required and must be explicitly false")
	}
	storeVal := strings.TrimSpace(string(*rawStore.Store))
	if storeVal != "false" {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: store must be false, got %s", storeVal)
	}

	// 5. Check unsupported controls and controls absent from canonical carrier.
	var unsuppCheck struct {
		PreviousResponseID   *json.RawMessage `json:"previous_response_id"`
		Truncation           *json.RawMessage `json:"truncation"`
		Background           *json.RawMessage `json:"background"`
		Include              *json.RawMessage `json:"include"`
		PresencePenalty      *json.RawMessage `json:"presence_penalty"`
		FrequencyPenalty     *json.RawMessage `json:"frequency_penalty"`
		StreamOptions        *json.RawMessage `json:"stream_options"`
		TopLogprobs          *json.RawMessage `json:"top_logprobs"`
		ServiceTier          *json.RawMessage `json:"service_tier"`
		SafetyIdentifier     *json.RawMessage `json:"safety_identifier"`
		PromptCacheKey       *json.RawMessage `json:"prompt_cache_key"`
		PromptCacheRetention *json.RawMessage `json:"prompt_cache_retention"`
		MaxToolCalls         *json.RawMessage `json:"max_tool_calls"`
	}
	if err := json.Unmarshal(readBytes, &unsuppCheck); err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: unmarshal unsupported check: %w", err)
	}
	isNonNull := func(raw *json.RawMessage) bool {
		if raw == nil {
			return false
		}
		t := bytes.TrimSpace(*raw)
		return len(t) > 0 && !bytes.Equal(t, []byte("null"))
	}
	if isNonNull(unsuppCheck.PreviousResponseID) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: previous_response_id requires continuation state")
	}
	if isNonNull(unsuppCheck.Truncation) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: truncation control is not supported")
	}
	if isNonNull(unsuppCheck.Background) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: background control is not supported")
	}
	if isNonNull(unsuppCheck.Include) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: include control is not supported")
	}
	if isNonNull(unsuppCheck.PresencePenalty) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: presence_penalty control is not supported")
	}
	if isNonNull(unsuppCheck.FrequencyPenalty) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: frequency_penalty control is not supported")
	}
	if isNonNull(unsuppCheck.StreamOptions) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: stream_options control is not supported")
	}
	if isNonNull(unsuppCheck.TopLogprobs) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: top_logprobs control is not supported")
	}
	if isNonNull(unsuppCheck.ServiceTier) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: service_tier control is not supported")
	}
	if isNonNull(unsuppCheck.SafetyIdentifier) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: safety_identifier control is not supported")
	}
	if isNonNull(unsuppCheck.PromptCacheKey) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: prompt_cache_key control is not supported")
	}
	if isNonNull(unsuppCheck.PromptCacheRetention) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: prompt_cache_retention control is not supported")
	}
	if isNonNull(unsuppCheck.MaxToolCalls) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: max_tool_calls control is not supported")
	}

	// 6. Split stream control from body bytes for protocol decode
	withoutStream, stream, err := splitStreamControl(readBytes)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: split stream control: %w", err)
	}

	// 7. Decode request via official protocol decoder
	wireParam, canonicalCall, err := proto.DecodeRequest(withoutStream, proto.DefaultLimits())
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: decode request: %w", err)
	}

	if !supportedTextFormat(wireParam.Text) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: unsupported text format")
	}

	if wireParam.Model == nil || strings.TrimSpace(*wireParam.Model) == "" {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: model is required")
	}
	model := strings.TrimSpace(*wireParam.Model)

	trimmedInput := bytes.TrimSpace(wireParam.Input)
	if len(trimmedInput) == 0 || bytes.Equal(trimmedInput, []byte(`""`)) || bytes.Equal(trimmedInput, []byte(`[]`)) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: input is required and cannot be empty")
	}

	// 8. Extract and validate metadata
	metadata, err := decodeMetadata(wireParam.Metadata)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: metadata: %w", err)
	}
	if sessionwire.HasSessionMetadata(metadata) {
		return frontendpipe.ProofOutput{}, largebody.ErrBodySessionMetadataRejected
	}

	// 9. Route selector precedence (Requirement 4.8, 17.6)
	sel := strings.TrimSpace(in.RouteSelector)
	if sel == "" && in.RouteFromBodyModel {
		if len(in.RoutePrefixes) == 0 {
			sel = model
		} else {
			sel = in.RoutePrefixes.InlineOrDefault(model, in.DefaultRouteSelector)
		}
	}
	if sel == "" {
		sel = in.DefaultRouteSelector
	}
	if sel == "" {
		sel = model
	}

	// 10. Extract session input
	sessIn, err := sessionwire.BuildSessionInput(in.Headers, metadata, sessionwire.SessionInputOptions{
		RejectBodyMetadata: true,
		MaxFactBytes:       frontendpipe.DefaultMaxSemanticFactBytes,
	})
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: session input: %w", err)
	}
	if sessIn.ClientSessionID != "" {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: session hint requires canonical decode")
	}

	// 11. Wire canonicalCall metadata for validation and turn shape
	canonicalCall.Route = lipapi.RouteIntent{Selector: sel}
	deliveryMode := lipapi.DeliveryModeFromClientStream(stream)
	canonicalCall.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenResponsesCreate,
		DeliveryMode:  deliveryMode,
		TransportMode: lipapi.PreferredTransportMode(deliveryMode),
	}
	if in.Headers != nil {
		canonicalCall.Invocation.ClientUserAgent = strings.TrimSpace(in.Headers.Get("User-Agent"))
		sessionwire.ApplyAuthoritativeHeaders(&canonicalCall.Session, in.Headers)
	}
	if metadata != nil {
		canonicalCall.Session.Metadata = metadata
	}

	if err := canonicalCall.Validate(); err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: canonical call validate: %w", err)
	}

	// 12. Build turn shape
	turnShape, err := largebody.ClientTurnShapeFromCall(&canonicalCall, frontendpipe.DefaultMaxSemanticFactBytes)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: turn shape: %w", err)
	}

	// 13. Derive canonical identity digest
	digest := largebody.CanonicalCallIdentity(&canonicalCall)

	// 14. Compute source digest
	var sumArr [32]byte
	copy(sumArr[:], hasher.Sum(nil))
	sourceDigest := largebody.NewSourceDigest(sumArr)

	var maxTokens int64
	if wireParam.MaxOutputTokens != nil {
		maxTokens = int64(*wireParam.MaxOutputTokens)
	}

	cancellationID := ""
	if sessIn.ALegID != "" {
		cancellationID = frontendpipe.FormatOpenAICancellationCarrier(sessIn.ALegID, sessIn.AuthoritativeSessionID)
	}
	if cancellationID == "" {
		cancellationID = "resp_" + digest.Token()
	}

	seeds := frontendpipe.NewResponseStateSeeds(
		digest,
		"",
		sel,
		model,
		stream,
		sessIn,
		cancellationID,
	)

	proof := largebody.Proof{
		ProfileID:       ProfileID,
		Operation:       lipapi.OperationOpenResponsesCreate,
		Delivery:        deliveryMode,
		RouteSelector:   sel,
		ClientModel:     model,
		MaxOutputTokens: maxTokens,
		Facts: largebody.ProtocolFacts{
			RequirementsID: ProfileID,
			ControlCount:   int64(len(wireParam.Tools)),
		},
		Mode:      largebody.BodyModeIdentityJSON,
		Rewrite:   rewrite,
		ModelSpan: modelSpan,
		Identity:  digest,
		Turn:      turnShape,
		Session:   sessIn,
		Source:    sourceDigest,
		BodyBytes: bodyBytes,
	}

	proofOut := frontendpipe.ProofOutput{
		State: frontendpipe.FrontendWireState{
			ProfileID: ProfileID,
			Proof:     proof,
			Seeds:     seeds,
		},
	}

	if err := proofOut.Validate(frontendpipe.DefaultMaxSemanticFactBytes); err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: validate proof output: %w", err)
	}

	return proofOut, nil
}

var _ frontendpipe.FrontendProfile = (*Profile)(nil)
