package openailegacy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonpresence"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonshape"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
	frontendlimits "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/limits"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ProfileID is the static identifier for the certified OpenAI Chat fast-path profile (Requirements 4, 17).
const ProfileID = "openai_chat_v1"

// Profile implements frontendpipe.FrontendProfile for OpenAI Chat Completions create (POST /v1/chat/completions).
type Profile struct{}

// NewProfile returns an initialized OpenAI Chat frontend profile.
func NewProfile() *Profile {
	return &Profile{}
}

// ProfileID returns the static identifier for this certified profile.
func (p *Profile) ProfileID() string {
	return ProfileID
}

var chatSupportedBodyKeys = map[string]bool{
	"model":               true,
	"messages":            true,
	"stream":              true,
	"tools":               true,
	"tool_choice":         true,
	"temperature":         true,
	"top_p":               true,
	"max_tokens":          true,
	"parallel_tool_calls": true,
	"reasoning_effort":    true,
	"verbosity":           true,
	"stream_options":      true,
	"metadata":            true,
}

// CompileProof compiles protocol proof and response seeds from the
// captured request replay under decode admission (Requirements 4, 13, 14, 16, 17).
func (p *Profile) CompileProof(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
	// 1. Path verification
	if !strings.HasSuffix(in.URLPath, "/chat/completions") && in.URLPath != "/chat/completions" {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: unsupported url path")
	}

	if in.Source == nil {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: nil replay source")
	}

	rc, err := in.Source.Open()
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: open replay source: %w", err)
	}
	defer rc.Close()

	readBytes, err := io.ReadAll(rc)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: read replay source: %w", err)
	}

	bodyBytes := in.BodyBytes
	if bodyBytes <= 0 {
		bodyBytes = int64(len(readBytes))
	}

	// 2. Scan with jsonshape.Scanner to enforce:
	//    - RejectDuplicateNames (canonical-only if duplicate keys)
	//    - Track exact byte span for "model"
	//    - Detect unknown or unsupported top-level keys (canonical-only)
	tracker := jsonshape.NewTopLevelSpanTracker("model")
	var unknownKey string
	handler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
		if e.TopLevel && e.Type == jsonshape.EventKey {
			if !chatSupportedBodyKeys[e.Key] {
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
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: json scanner feed: %w", err)
		}
	}
	if _, err := scanner.Finish(); err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: json scanner finish: %w", err)
	}

	if unknownKey != "" {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: unsupported or unknown body key %q", unknownKey)
	}

	// 3. Verify top-level "model" span
	modelSpanRaw, hasModel := tracker.Span("model")
	if !hasModel || modelSpanRaw.Length == 0 {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: model is required")
	}
	modelSpan := largebody.Span{Offset: modelSpanRaw.Offset, Length: modelSpanRaw.Length}
	rewrite, err := largebody.NewModelTokenRewrite(modelSpan)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: model rewrite: %w", err)
	}

	// 4. Parse JSON into wireCreate
	var w wireCreate
	if err := json.Unmarshal(readBytes, &w); err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: unmarshal: %w", err)
	}

	model := strings.TrimSpace(w.Model)
	if model == "" {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: model is required")
	}
	if len(w.Messages) == 0 {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: messages is required")
	}

	// 5. Check limits
	if err := frontendlimits.Count("messages", len(w.Messages), frontendlimits.MaxMessages); err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: %w", err)
	}
	if len(w.Metadata) > 0 {
		if err := frontendlimits.Count("metadata", len(w.Metadata), frontendlimits.MaxMetadata); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: %w", err)
		}
		if err := sessionwire.ValidateMetadata(w.Metadata); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: %w", err)
		}
		// Requirement 14.2, 17.5: Reject body-carried LIP session metadata
		if sessionwire.HasSessionMetadata(w.Metadata) {
			return frontendpipe.ProofOutput{}, largebody.ErrBodySessionMetadataRejected
		}
	}

	// 6. Parse and normalize messages (declining any malformed/alias/drop shapes to canonical)
	msgs := make([]lipapi.Message, 0, len(w.Messages))
	for i, rawMsg := range w.Messages {
		m, err := parseProfileMessage(rawMsg)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: messages[%d]: %w", i, err)
		}
		msgs = append(msgs, m)
	}

	// 7. Parse tools, tool_choice, verbosity
	tools, err := parseTools(w.Tools)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: tools: %w", err)
	}
	toolChoice, err := parseToolChoice(w.ToolChoice)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: tool_choice: %w", err)
	}
	verbosity, err := lipapi.ParseVerbosityLevel(w.Verbosity)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: verbosity: %w", err)
	}

	// 8. Build extensions
	modelRaw, err := json.Marshal(model)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: marshal model: %w", err)
	}
	ext := map[string]json.RawMessage{extModelJSONKey: modelRaw}
	if b, err := json.Marshal(openrouterwire.FlavorChat); err == nil {
		ext[openrouterwire.ExtUpstreamFlavor] = b
	}
	if jsonpresence.IsPresentNonNullJSON(w.StreamOptions) {
		ext[extStreamOptsJSONKey] = w.StreamOptions
	}
	if in.Headers != nil {
		openrouterwire.CaptureHeaders(in.Headers, ext)
	}

	// 9. Resolve route selector precedence (Requirement 4.8, 17.6)
	sel := strings.TrimSpace(in.RouteSelector)
	if sel == "" && in.RouteFromBodyModel {
		sel = in.RoutePrefixes.InlineOrDefault(model, in.DefaultRouteSelector)
	}
	if sel == "" {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: route selector is required")
	}

	// 10. Extract session input
	sessIn, err := sessionwire.BuildSessionInput(in.Headers, w.Metadata, sessionwire.SessionInputOptions{
		RejectBodyMetadata: true,
		MaxFactBytes:       frontendpipe.DefaultMaxSemanticFactBytes,
	})
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: session input: %w", err)
	}

	// Requirement 16.2, 16.7: X-LIP-Session-Hint feeds ClientSessionID into wire digest
	// while canonical pre-core decode never sets it. To prevent identity divergence,
	// requests carrying a client session hint must decline to canonical processing.
	if sessIn.ClientSessionID != "" {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: session hint requires canonical decode")
	}

	// 11. Build turn shape
	turnShape, err := largebody.ClientTurnShapeFromCall(&lipapi.Call{
		Messages: msgs,
	}, frontendpipe.DefaultMaxSemanticFactBytes)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: turn shape: %w", err)
	}

	// 12. Derive canonical identity digest
	idCfg := largebody.CallIdentityConfig{
		Session: lipapi.SessionRef{
			AuthoritativeSessionID: sessIn.AuthoritativeSessionID,
			ClientSessionID:        sessIn.ClientSessionID,
			ResumeToken:            sessIn.ResumeToken.Reveal(),
		},
		SessionInput:  sessIn,
		RouteSelector: sel,
		Tools:         tools,
		ToolChoice:    toolChoice,
		Options: lipapi.GenerationOptions{
			Temperature:       w.Temperature,
			TopP:              w.TopP,
			MaxOutputTokens:   w.MaxTokens,
			ParallelToolCalls: w.ParallelToolCalls,
			ReasoningEffort:   strings.TrimSpace(w.ReasoningEffort),
			Verbosity:         verbosity,
		},
		Extensions: ext,
	}
	idWriter, err := largebody.NewCallIdentityWriter(idCfg)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: identity writer: %w", err)
	}
	if msgs != nil {
		if err := idWriter.StartMessages(); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: start messages: %w", err)
		}
		for _, msg := range msgs {
			if err := idWriter.AddMessage(msg); err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: add message: %w", err)
			}
		}
	}
	digest, err := idWriter.Digest()
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: digest: %w", err)
	}

	// 13. Compute source digest
	var sumArr [32]byte
	copy(sumArr[:], hasher.Sum(nil))
	sourceDigest := largebody.NewSourceDigest(sumArr)

	var maxTokens int64
	if w.MaxTokens != nil {
		maxTokens = int64(*w.MaxTokens)
	}

	cancellationID := ""
	if sessIn.ALegID != "" {
		cancellationID = frontendpipe.FormatOpenAICancellationCarrier(sessIn.ALegID, sessIn.AuthoritativeSessionID)
	}
	if cancellationID == "" {
		cancellationID = "chatcmpl_" + digest.Token()
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
		Operation:       lipapi.OperationOpenAIChatCompletions,
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
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: validate proof output: %w", err)
	}

	return proofOut, nil
}

func parseProfileMessage(raw json.RawMessage) (lipapi.Message, error) {
	var probe struct {
		Role             string          `json:"role"`
		Content          json.RawMessage `json:"content"`
		ToolCallID       string          `json:"tool_call_id"`
		ToolCalls        json.RawMessage `json:"tool_calls"`
		FunctionCall     json.RawMessage `json:"function_call"`
		ReasoningContent *string         `json:"reasoning_content"`
		Reasoning        *string         `json:"reasoning"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return lipapi.Message{}, fmt.Errorf("message json: %w", err)
	}

	roleStr := strings.TrimSpace(strings.ToLower(probe.Role))
	// Requirement 17.3: Developer role is normalized to system by canonical encoder; keep canonical-only.
	if roleStr == "developer" {
		return lipapi.Message{}, errors.New("developer role requires canonical normalization")
	}
	role, err := mapRole(probe.Role)
	if err != nil {
		return lipapi.Message{}, fmt.Errorf("role: %w", err)
	}

	switch role {
	case lipapi.RoleTool:
		if strings.TrimSpace(probe.ToolCallID) == "" {
			return lipapi.Message{}, fmt.Errorf("malformed tool message requires canonical repair: %w", errSkipMalformedHistory)
		}
		trimmed := bytes.TrimSpace(probe.Content)
		if len(trimmed) == 0 || trimmed[0] != '"' {
			// Non-string tool content is re-encoded as string by canonical backend; keep canonical-only.
			return lipapi.Message{}, errors.New("non-string tool content requires canonical encoding")
		}
		content, err := parseToolMessageContent(probe.Content)
		if err != nil {
			return lipapi.Message{}, fmt.Errorf("tool message content: %w", err)
		}
		rawJSON, err := json.Marshal(content)
		if err != nil {
			return lipapi.Message{}, fmt.Errorf("tool message marshal: %w", err)
		}
		return lipapi.Message{
			Role: lipapi.RoleTool,
			Parts: []lipapi.Part{{
				Kind:       lipapi.PartToolResult,
				ToolCallID: strings.TrimSpace(probe.ToolCallID),
				Content:    rawJSON,
			}},
		}, nil

	case lipapi.RoleAssistant:
		if len(probe.FunctionCall) > 0 {
			return lipapi.Message{}, errors.New("legacy function_call requires canonical normalization")
		}
		if probe.Reasoning != nil {
			return lipapi.Message{}, errors.New("reasoning alias requires canonical normalization")
		}
		if jsonpresence.IsPresentNonNullJSON(probe.ToolCalls) {
			if err := frontendlimits.Bytes("tool_calls", len(probe.ToolCalls), frontendlimits.MaxRawJSONPayload); err != nil {
				return lipapi.Message{}, err
			}
			var rawCalls []json.RawMessage
			if err := json.Unmarshal(probe.ToolCalls, &rawCalls); err != nil {
				return lipapi.Message{}, fmt.Errorf("tool_calls: %w", err)
			}
			for tcIdx, rc := range rawCalls {
				if !json.Valid(rc) {
					return lipapi.Message{}, errors.New("invalid tool_calls entry")
				}
				var wire struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				}
				if err := json.Unmarshal(rc, &wire); err != nil {
					return lipapi.Message{}, fmt.Errorf("tool_calls[%d]: %w", tcIdx, err)
				}
				if strings.TrimSpace(wire.ID) == "" || strings.TrimSpace(wire.Function.Name) == "" {
					return lipapi.Message{}, fmt.Errorf("tool_calls[%d]: malformed tool call requires canonical repair: %w", tcIdx, errSkipMalformedHistory)
				}
				if jsonpresence.IsPresentNonNullJSON(wire.Function.Arguments) {
					trimmedArgs := bytes.TrimSpace(wire.Function.Arguments)
					if len(trimmedArgs) == 0 || trimmedArgs[0] != '"' {
						return lipapi.Message{}, fmt.Errorf("tool_calls[%d]: non-string tool_calls arguments requires canonical encoding", tcIdx)
					}
				}
			}
		}
		parts, err := parseAssistantParts(probe.Content, probe.ToolCalls, probe.FunctionCall, probe.ReasoningContent, probe.Reasoning)
		if err != nil {
			if errors.Is(err, errEmptyAssistantMessage) {
				return lipapi.Message{}, fmt.Errorf("empty assistant message requires canonical drop: %w", err)
			}
			if errors.Is(err, errSkipMalformedHistory) {
				return lipapi.Message{}, fmt.Errorf("malformed history requires canonical repair: %w", err)
			}
			return lipapi.Message{}, fmt.Errorf("assistant message: %w", err)
		}
		return lipapi.Message{Role: lipapi.RoleAssistant, Parts: parts}, nil

	default:
		parts, err := parseChatContent(probe.Content)
		if err != nil {
			return lipapi.Message{}, fmt.Errorf("message content: %w", err)
		}
		return lipapi.Message{Role: role, Parts: parts}, nil
	}
}

var _ frontendpipe.FrontendProfile = (*Profile)(nil)
