package openairesponses

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
	frontendlimits "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/limits"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ProfileID is the static identifier for the certified OpenAI Responses fast-path profile (Requirements 4, 17).
const ProfileID = "openai_responses_v1"

// Profile implements frontendpipe.FrontendProfile for OpenAI Responses create (POST /v1/responses).
type Profile struct{}

// NewProfile returns an initialized OpenAI Responses frontend profile.
func NewProfile() *Profile {
	return &Profile{}
}

// ProfileID returns the static identifier for this certified profile.
func (p *Profile) ProfileID() string {
	return ProfileID
}

// CompileProof compiles protocol proof and response seeds from the
// captured request replay under decode admission (Requirements 4, 13, 14, 16, 17).
func (p *Profile) CompileProof(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
	// 1. Path verification
	if !strings.HasSuffix(in.URLPath, "/responses") && in.URLPath != "/responses" {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: unsupported url path")
	}

	if in.Source == nil {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: nil replay source")
	}

	rc, err := in.Source.Open()
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: open replay source: %w", err)
	}
	defer rc.Close()

	readBytes, err := io.ReadAll(rc)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: read replay source: %w", err)
	}

	bodyBytes := in.BodyBytes
	if bodyBytes <= 0 {
		bodyBytes = int64(len(readBytes))
	}

	// 2. Scan with jsonshape.Scanner to enforce:
	//    - RejectDuplicateNames (canonical-only if duplicate keys)
	//    - Track exact byte span for "model"
	//    - Detect unknown top-level keys (canonical-only if unknown)
	//    - Detect unsupported control keys: "store", "previous_response_id", "truncation" (canonical-only)
	tracker := jsonshape.NewTopLevelSpanTracker("model")
	var unknownKey string
	var hasUnsupportedControl bool
	handler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
		if e.TopLevel && e.Type == jsonshape.EventKey {
			if !responsesKnownBodyKeys[e.Key] {
				unknownKey = e.Key
			}
			if e.Key == "store" || e.Key == "previous_response_id" || e.Key == "truncation" {
				hasUnsupportedControl = true
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
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: json scanner feed: %w", err)
		}
	}
	if _, err := scanner.Finish(); err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: json scanner finish: %w", err)
	}

	if unknownKey != "" {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: unknown body key %q", unknownKey)
	}
	if hasUnsupportedControl {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: unsupported control key (store/previous_response_id/truncation)")
	}

	// 3. Verify top-level "model" span
	modelSpanRaw, hasModel := tracker.Span("model")
	if !hasModel || modelSpanRaw.Length == 0 {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: model is required")
	}
	modelSpan := largebody.Span{Offset: modelSpanRaw.Offset, Length: modelSpanRaw.Length}
	rewrite, err := largebody.NewModelTokenRewrite(modelSpan)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: model rewrite: %w", err)
	}

	// 4. Parse JSON into wireCreate
	var w wireCreate
	if err := json.Unmarshal(readBytes, &w); err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: unmarshal: %w", err)
	}

	model := strings.TrimSpace(w.Model)
	if model == "" {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: model is required")
	}
	if len(w.Input) == 0 {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: input is required")
	}

	// 5. Check metadata
	if len(w.Metadata) > 0 {
		if err := frontendlimits.Count("metadata", len(w.Metadata), frontendlimits.MaxMetadata); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: %w", err)
		}
		// Requirement 14.2, 17.5: Reject body-carried LIP session metadata
		if sessionwire.HasSessionMetadata(w.Metadata) {
			return frontendpipe.ProofOutput{}, largebody.ErrBodySessionMetadataRejected
		}
	}

	// 6. Resolve route selector precedence (Requirement 4.8, 17.6)
	sel := strings.TrimSpace(in.RouteSelector)
	if sel == "" && in.RouteFromBodyModel {
		sel = in.RoutePrefixes.InlineOrDefault(model, in.DefaultRouteSelector)
	}
	if sel == "" {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: route selector is required")
	}

	// 7. Parse instructions
	instructions, err := parseInstructions(w.Instructions)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: instructions: %w", err)
	}

	// 8. Parse input
	trimmedInput := bytes.TrimSpace(w.Input)
	if len(trimmedInput) == 0 {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: input is empty")
	}
	var msgs []lipapi.Message
	switch trimmedInput[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmedInput, &s); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: input string: %w", err)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return frontendpipe.ProofOutput{}, errors.New("openairesponses: input string is empty")
		}
		msgs = []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart(s)},
		}}
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(trimmedInput, &items); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: input array: %w", err)
		}
		if err := frontendlimits.Count("input", len(items), frontendlimits.MaxMessages); err != nil {
			return frontendpipe.ProofOutput{}, err
		}
		msgs = make([]lipapi.Message, 0, len(items))
		for i, it := range items {
			m, err := parseInputItem(it)
			if err != nil {
				// Requirement 17.3: Malformed histories/aliases that current decoders repair/drop/normalize are canonical-only.
				if errors.Is(err, errSkipMalformedHistory) {
					return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: malformed history requires canonical repair: %w", err)
				}
				return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: input[%d]: %w", i, err)
			}
			for _, part := range m.Parts {
				if part.Kind != lipapi.PartToolResult {
					continue
				}
				trimmedContent := bytes.TrimSpace(part.Content)
				if len(trimmedContent) == 0 || trimmedContent[0] != '"' {
					// Requirement 9/17: non-string function_call_output outputs are
					// re-encoded as strings by the canonical backend while the wire
					// path forwards raw bytes; keep that shape canonical-only.
					return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: input[%d]: non-string function_call_output output requires canonical encoding", i)
				}
			}
			msgs = append(msgs, m)
		}
	default:
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: input must be a string or array")
	}

	// 9. Parse tools and options
	tools, err := parseTools(w.Tools)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: tools: %w", err)
	}
	toolChoice, err := parseToolChoice(w.ToolChoice)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: tool_choice: %w", err)
	}
	verbosity, remainingText, err := parseTextConfig(w.Text)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: text: %w", err)
	}

	// 10. Build extensions
	modelRaw, err := json.Marshal(model)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: marshal model: %w", err)
	}
	ext := map[string]json.RawMessage{extModelJSONKey: modelRaw}
	if b, err := json.Marshal(openrouterwire.FlavorResponses); err == nil {
		ext[openrouterwire.ExtUpstreamFlavor] = b
	}
	if len(remainingText) > 0 {
		ext[openrouterwire.ExtraBodyExtPrefix+"text"] = remainingText
	}
	if in.Headers != nil {
		openrouterwire.CaptureHeaders(in.Headers, ext)
	}

	// 11. Extract session input
	sessIn, err := sessionwire.BuildSessionInput(in.Headers, w.Metadata, sessionwire.SessionInputOptions{
		RejectBodyMetadata: true,
		MaxFactBytes:       frontendpipe.DefaultMaxSemanticFactBytes,
	})
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: session input: %w", err)
	}

	// Requirement 16.2, 16.7: X-LIP-Session-Hint feeds ClientSessionID into wire digest
	// while canonical pre-core decode never sets it. To prevent identity divergence,
	// requests carrying a client session hint must decline to canonical processing.
	if sessIn.ClientSessionID != "" {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: session hint requires canonical decode")
	}

	// 12. Build turn shape
	turnShape, err := largebody.ClientTurnShapeFromCall(&lipapi.Call{
		Instructions: instructions,
		Messages:     msgs,
	}, frontendpipe.DefaultMaxSemanticFactBytes)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: turn shape: %w", err)
	}

	// 13. Derive canonical identity digest
	idCfg := largebody.CallIdentityConfig{
		Session: lipapi.SessionRef{
			AuthoritativeSessionID: sessIn.AuthoritativeSessionID,
			ClientSessionID:        sessIn.ClientSessionID,
			ResumeToken:            sessIn.ResumeToken.Reveal(),
		},
		SessionInput:  sessIn,
		RouteSelector: sel,
		Instructions:  instructions,
		Tools:         tools,
		ToolChoice:    toolChoice,
		Options: lipapi.GenerationOptions{
			Temperature:       w.Temperature,
			TopP:              w.TopP,
			MaxOutputTokens:   w.MaxOut,
			ParallelToolCalls: w.ParallelTools,
			Verbosity:         verbosity,
		},
		Extensions: ext,
	}
	idWriter, err := largebody.NewCallIdentityWriter(idCfg)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: identity writer: %w", err)
	}
	if msgs != nil {
		if err := idWriter.StartMessages(); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: start messages: %w", err)
		}
		for _, msg := range msgs {
			if err := idWriter.AddMessage(msg); err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: add message: %w", err)
			}
		}
	}
	digest, err := idWriter.Digest()
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: digest: %w", err)
	}

	// 14. Compute source digest
	var sumArr [32]byte
	copy(sumArr[:], hasher.Sum(nil))
	sourceDigest := largebody.NewSourceDigest(sumArr)

	var maxTokens int64
	if w.MaxOut != nil {
		maxTokens = int64(*w.MaxOut)
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
		w.Stream,
		sessIn,
		cancellationID,
	)

	proof := largebody.Proof{
		ProfileID:       ProfileID,
		Operation:       lipapi.OperationOpenAIResponses,
		Delivery:        lipapi.DeliveryModeFromClientStream(w.Stream),
		RouteSelector:   sel,
		ClientModel:     model,
		MaxOutputTokens: maxTokens,
		Facts: largebody.ProtocolFacts{
			RequirementsID: ProfileID,
			ControlCount:   int64(len(tools)),
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
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: validate proof output: %w", err)
	}

	return proofOut, nil
}

var _ frontendpipe.FrontendProfile = (*Profile)(nil)
