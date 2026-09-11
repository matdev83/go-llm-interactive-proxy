package openailegacy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
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

type budgetBoundedWriter struct {
	buf       *bytes.Buffer
	total     *int64
	maxBudget int64
}

func (w *budgetBoundedWriter) Write(p []byte) (int, error) {
	if *w.total+int64(len(p)) > w.maxBudget {
		return 0, fmt.Errorf("%w: envelope fact budget exceeded (%d > %d)",
			largebody.ErrSemanticFactBudgetExceeded, *w.total+int64(len(p)), w.maxBudget)
	}
	*w.total += int64(len(p))
	return w.buf.Write(p)
}

type chunkHookReader struct {
	r      io.Reader
	onRead func(chunk []byte)
}

func (c *chunkHookReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 && c.onRead != nil {
		c.onRead(p[:n])
	}
	return n, err
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
// Uses the streaming proof pass without allocating or retaining the full request body.
func (p *Profile) CompileProof(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
	// 1. Path verification
	if !strings.HasSuffix(in.URLPath, "/chat/completions") && in.URLPath != "/chat/completions" {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: unsupported url path")
	}

	if in.Source == nil {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: nil replay source")
	}

	scanCtx := in.Ctx
	if scanCtx == nil {
		scanCtx = ctx
	}

	bodyBytes := in.BodyBytes
	if bodyBytes <= 0 {
		bodyBytes = in.Source.Size()
	}

	rc, err := in.Source.Open()
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: open replay source: %w", err)
	}
	defer rc.Close()

	// 2. Pass 1: Streaming scan over replay bytes using jsonshape.Scanner + SHA-256 in fixed buffers (no ReadAll).
	// Enforces:
	//   - RejectDuplicateNames (canonical-only if duplicate keys)
	//   - Track exact byte span for "model"
	//   - Detect unknown top-level keys (canonical-only if unknown)
	//   - Extract small envelope fields (model, stream, options, tools, etc.) bounded by DefaultMaxSemanticFactBytes
	//   - Detect messages presence and shape without materializing large messages in memory
	tracker := jsonshape.NewTopLevelSpanTracker("model")
	var unknownKey string

	var modelBuf bytes.Buffer
	var reasoningEffortBuf bytes.Buffer
	var verbosityBuf bytes.Buffer
	var toolChoiceStrBuf bytes.Buffer

	var stream bool
	var parallelTools *bool
	var temperature *float64
	var topP *float64
	var maxTokens *int

	var hasMessages bool
	var messagesIsArray bool
	var toolChoiceIsString bool

	// For large array tracking:
	var arrayItemCount int
	var largeArrayRoles []lipapi.Role
	var largeArrayDeclineErr error
	var currentItemRole lipapi.Role = lipapi.RoleUser
	var currentItemHasContent bool
	var currentItemHasRole bool
	var itemRoleBuf bytes.Buffer
	var itemTypeBuf bytes.Buffer

	// Composite field buffers (tools, tool_choice object, stream_options, metadata)
	// Semantic-fact budget enforcement point 1: Bounded by DefaultMaxSemanticFactBytes (256 KiB).
	fieldBufs := make(map[string]*bytes.Buffer)
	for _, k := range []string{"tools", "tool_choice", "stream_options", "metadata"} {
		fieldBufs[k] = &bytes.Buffer{}
	}
	var messagesArrayBuf bytes.Buffer
	messagesArrayIsLarge := bodyBytes > 2*frontendpipe.DefaultMaxSemanticFactBytes
	var totalFactBytes int64

	// Chunk tracking for composite extraction and number tokens across chunk boundaries
	var currentChunk []byte
	var prevChunkTail []byte
	var chunkStartOffset int64
	var totalBytesRead int64

	extractNumberStr := func(e jsonshape.Event) (string, error) {
		// 1. Fully contained in current chunk
		if e.Offset >= chunkStartOffset && e.Offset+e.Length <= chunkStartOffset+int64(len(currentChunk)) {
			start := e.Offset - chunkStartOffset
			return string(currentChunk[start : start+e.Length]), nil
		}
		// 2. Starts in prevChunkTail and completes in currentChunk
		if e.Offset < chunkStartOffset && e.Offset+e.Length <= chunkStartOffset+int64(len(currentChunk)) {
			prevLen := chunkStartOffset - e.Offset
			if prevLen > 0 && prevLen <= int64(len(prevChunkTail)) {
				startInPrev := int64(len(prevChunkTail)) - prevLen
				part1 := prevChunkTail[startInPrev:]
				part2 := currentChunk[:e.Offset+e.Length-chunkStartOffset]
				return string(part1) + string(part2), nil
			}
		}
		// 3. Spanning shape not fully contained in buffers -> fail-closed decline
		return "", fmt.Errorf("openailegacy: %s spanning chunk boundary requires canonical decode", e.Key)
	}

	var capturingKey string
	var capturingBuf *bytes.Buffer
	var capturingStart int64

	var capturingMessagesArray bool
	var messagesArrayStart int64

	handler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
		if e.TopLevel && e.Type == jsonshape.EventKey {
			if !chatSupportedBodyKeys[e.Key] {
				unknownKey = e.Key
			}
			if e.Key == "messages" {
				hasMessages = true
			}
		}

		if e.TopLevel && e.Type != jsonshape.EventKey && e.Type != jsonshape.EventArrayEnd && e.Type != jsonshape.EventObjectEnd {
			switch e.Key {
			case "model":
				if e.Type != jsonshape.EventString {
					return errors.New("openailegacy: model must be a string")
				}
			case "messages":
				if e.Type == jsonshape.EventArrayStart {
					messagesIsArray = true
					capturingMessagesArray = true
					messagesArrayStart = e.Offset
					messagesArrayBuf.Reset()
				} else {
					return errors.New("openailegacy: messages must be an array")
				}
			case "stream":
				if e.Type == jsonshape.EventTrue {
					stream = true
				} else if e.Type == jsonshape.EventFalse {
					stream = false
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: stream must be a boolean")
				}
			case "tools":
				if e.Type == jsonshape.EventArrayStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: tools must be an array")
				}
			case "tool_choice":
				if e.Type == jsonshape.EventString {
					toolChoiceIsString = true
				} else if e.Type == jsonshape.EventObjectStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: tool_choice must be a string or object")
				}
			case "parallel_tool_calls":
				if e.Type == jsonshape.EventTrue {
					v := true
					parallelTools = &v
				} else if e.Type == jsonshape.EventFalse {
					v := false
					parallelTools = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: parallel_tool_calls must be a boolean")
				}
			case "temperature":
				if e.Type == jsonshape.EventNumber {
					numStr, err := extractNumberStr(e)
					if err != nil {
						return err
					}
					v, err := strconv.ParseFloat(numStr, 64)
					if err != nil {
						return fmt.Errorf("openailegacy: temperature: %w", err)
					}
					temperature = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: temperature must be a number")
				}
			case "top_p":
				if e.Type == jsonshape.EventNumber {
					numStr, err := extractNumberStr(e)
					if err != nil {
						return err
					}
					v, err := strconv.ParseFloat(numStr, 64)
					if err != nil {
						return fmt.Errorf("openailegacy: top_p: %w", err)
					}
					topP = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: top_p must be a number")
				}
			case "max_tokens":
				if e.Type == jsonshape.EventNumber {
					numStr, err := extractNumberStr(e)
					if err != nil {
						return err
					}
					v, err := strconv.Atoi(numStr)
					if err != nil {
						return fmt.Errorf("openailegacy: max_tokens must be an integer: %w", err)
					}
					maxTokens = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: max_tokens must be an integer")
				}
			case "reasoning_effort":
				if e.Type != jsonshape.EventString && e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: reasoning_effort must be a string")
				}
			case "verbosity":
				if e.Type != jsonshape.EventString && e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: verbosity must be a string")
				}
			case "stream_options":
				if e.Type == jsonshape.EventObjectStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: stream_options must be an object")
				}
			case "metadata":
				if e.Type == jsonshape.EventObjectStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: metadata must be an object")
				}
			}
		}

		if e.TopLevel {
			if e.Type == jsonshape.EventArrayEnd {
				if e.Key == "messages" {
					if capturingMessagesArray && !messagesArrayIsLarge {
						start := max(chunkStartOffset, messagesArrayStart)
						end := e.Offset + e.Length
						if start < end && end <= chunkStartOffset+int64(len(currentChunk)) {
							slice := currentChunk[start-chunkStartOffset : end-chunkStartOffset]
							if int64(messagesArrayBuf.Len()+len(slice)) > frontendpipe.DefaultMaxSemanticFactBytes {
								messagesArrayIsLarge = true
								messagesArrayBuf.Reset()
							} else {
								messagesArrayBuf.Write(slice)
							}
						}
					}
					capturingMessagesArray = false
				} else if e.Key == capturingKey && capturingBuf != nil {
					start := max(chunkStartOffset, capturingStart)
					end := e.Offset + e.Length
					if start < end && end <= chunkStartOffset+int64(len(currentChunk)) {
						slice := currentChunk[start-chunkStartOffset : end-chunkStartOffset]
						capturingBuf.Write(slice)
						totalFactBytes += int64(len(slice))
						// Semantic-fact budget enforcement point 1: Bounded by DefaultMaxSemanticFactBytes
						if totalFactBytes > frontendpipe.DefaultMaxSemanticFactBytes {
							return fmt.Errorf("%w: envelope fact budget exceeded (%d > %d)",
								largebody.ErrSemanticFactBudgetExceeded, totalFactBytes, frontendpipe.DefaultMaxSemanticFactBytes)
						}
					}
					capturingKey = ""
					capturingBuf = nil
				}
			} else if e.Type == jsonshape.EventObjectEnd {
				if e.Key == capturingKey && capturingBuf != nil {
					start := max(chunkStartOffset, capturingStart)
					end := e.Offset + e.Length
					if start < end && end <= chunkStartOffset+int64(len(currentChunk)) {
						slice := currentChunk[start-chunkStartOffset : end-chunkStartOffset]
						capturingBuf.Write(slice)
						totalFactBytes += int64(len(slice))
						// Semantic-fact budget enforcement point 1: Bounded by DefaultMaxSemanticFactBytes
						if totalFactBytes > frontendpipe.DefaultMaxSemanticFactBytes {
							return fmt.Errorf("%w: envelope fact budget exceeded (%d > %d)",
								largebody.ErrSemanticFactBudgetExceeded, totalFactBytes, frontendpipe.DefaultMaxSemanticFactBytes)
						}
					}
					capturingKey = ""
					capturingBuf = nil
				}
			}
		}

		// Item 2: Inspect message array items inside messages
		if !e.TopLevel && len(e.Path) >= 1 && e.Path[0] == "messages" {
			if len(e.Path) == 1 {
				if e.Type == jsonshape.EventObjectStart {
					arrayItemCount++
					if arrayItemCount > frontendlimits.MaxMessages {
						return frontendlimits.Count("messages", arrayItemCount, frontendlimits.MaxMessages)
					}
					currentItemRole = lipapi.RoleUser
					currentItemHasContent = false
					currentItemHasRole = false
				} else if e.Type == jsonshape.EventObjectEnd {
					if !currentItemHasRole && largeArrayDeclineErr == nil {
						largeArrayDeclineErr = errors.New("openailegacy: message role is required")
					}
					if !currentItemHasContent && largeArrayDeclineErr == nil {
						largeArrayDeclineErr = errors.New("openailegacy: message content is required")
					}
					largeArrayRoles = append(largeArrayRoles, currentItemRole)
					currentItemRole = lipapi.RoleUser
					itemRoleBuf.Reset()
					itemTypeBuf.Reset()
				} else {
					if largeArrayDeclineErr == nil {
						largeArrayDeclineErr = errors.New("openailegacy: message array item must be an object")
					}
				}
			} else if len(e.Path) == 2 {
				if e.Type == jsonshape.EventKey {
					if e.Key != "role" && e.Key != "content" && e.Key != "type" && largeArrayDeclineErr == nil {
						largeArrayDeclineErr = fmt.Errorf("openailegacy: unsupported message item field %q requires canonical decode", e.Key)
					}
				} else if e.Type == jsonshape.EventString {
					switch e.Key {
					case "role":
						currentItemHasRole = true
						rStr := strings.TrimSpace(itemRoleBuf.String())
						switch rStr {
						case "user":
							currentItemRole = lipapi.RoleUser
						case "assistant":
							currentItemRole = lipapi.RoleAssistant
						case "system":
							currentItemRole = lipapi.RoleSystem
						case "developer":
							if largeArrayDeclineErr == nil {
								largeArrayDeclineErr = errors.New("openailegacy: developer role requires canonical normalization")
							}
						case "tool":
							if largeArrayDeclineErr == nil {
								largeArrayDeclineErr = errors.New("openailegacy: tool role requires canonical decode")
							}
						default:
							if largeArrayDeclineErr == nil {
								largeArrayDeclineErr = fmt.Errorf("openailegacy: unsupported role %q requires canonical decode", rStr)
							}
						}
					case "type":
						tStr := strings.TrimSpace(itemTypeBuf.String())
						if tStr != "" && tStr != "message" && largeArrayDeclineErr == nil {
							largeArrayDeclineErr = fmt.Errorf("openailegacy: unsupported message item type %q requires canonical decode", tStr)
						}
					case "content":
						currentItemHasContent = true
					}
				} else {
					if e.Key == "content" {
						if largeArrayDeclineErr == nil {
							largeArrayDeclineErr = errors.New("openailegacy: complex message content requires canonical decode")
						}
					} else if largeArrayDeclineErr == nil {
						largeArrayDeclineErr = fmt.Errorf("openailegacy: message item field %q must be a string", e.Key)
					}
				}
			} else {
				if largeArrayDeclineErr == nil {
					largeArrayDeclineErr = errors.New("openailegacy: complex nested message structure requires canonical decode")
				}
			}
		}

		return tracker.OnEvent(e)
	})

	strResolver := func(sctx jsonshape.StringContext) (io.Writer, error) {
		if sctx.TopLevel {
			switch sctx.Key {
			case "model":
				return &budgetBoundedWriter{buf: &modelBuf, total: &totalFactBytes, maxBudget: frontendpipe.DefaultMaxSemanticFactBytes}, nil
			case "reasoning_effort":
				return &budgetBoundedWriter{buf: &reasoningEffortBuf, total: &totalFactBytes, maxBudget: frontendpipe.DefaultMaxSemanticFactBytes}, nil
			case "verbosity":
				return &budgetBoundedWriter{buf: &verbosityBuf, total: &totalFactBytes, maxBudget: frontendpipe.DefaultMaxSemanticFactBytes}, nil
			case "tool_choice":
				return &budgetBoundedWriter{buf: &toolChoiceStrBuf, total: &totalFactBytes, maxBudget: frontendpipe.DefaultMaxSemanticFactBytes}, nil
			}
		}
		if len(sctx.Path) >= 1 && sctx.Path[0] == "messages" {
			if sctx.Key == "role" {
				itemRoleBuf.Reset()
				return &itemRoleBuf, nil
			}
			if sctx.Key == "type" {
				itemTypeBuf.Reset()
				return &itemTypeBuf, nil
			}
		}
		return nil, nil
	}

	hasher := sha256.New()
	scannerLimits := jsonshape.Limits{
		RejectDuplicateNames: true, // Item 6: set explicitly to prevent silent relaxation
		MaxBytes:             bodyBytes,
		MaxDepth:             128,
	}

	scanner := jsonshape.NewScanner(
		scanCtx,
		scannerLimits,
		jsonshape.WithEventHandler(handler),
		jsonshape.WithStringWriterResolver(strResolver),
	)

	onChunk := func(chunk []byte) error {
		chunkEndOffset := chunkStartOffset + int64(len(chunk))

		if capturingKey != "" && capturingBuf != nil {
			start := max(chunkStartOffset, capturingStart)
			if start < chunkEndOffset {
				slice := chunk[start-chunkStartOffset:]
				capturingBuf.Write(slice)
				totalFactBytes += int64(len(slice))
				// Semantic-fact budget enforcement point 1: Bounded by DefaultMaxSemanticFactBytes
				if totalFactBytes > frontendpipe.DefaultMaxSemanticFactBytes {
					return fmt.Errorf("%w: envelope fact budget exceeded (%d > %d)",
						largebody.ErrSemanticFactBudgetExceeded, totalFactBytes, frontendpipe.DefaultMaxSemanticFactBytes)
				}
				capturingStart = chunkEndOffset
			}
		}

		if capturingMessagesArray && !messagesArrayIsLarge {
			start := max(chunkStartOffset, messagesArrayStart)
			if start < chunkEndOffset {
				slice := chunk[start-chunkStartOffset:]
				if int64(messagesArrayBuf.Len()+len(slice)) > frontendpipe.DefaultMaxSemanticFactBytes {
					messagesArrayIsLarge = true
					messagesArrayBuf.Reset()
				} else {
					messagesArrayBuf.Write(slice)
					messagesArrayStart = chunkEndOffset
				}
			}
		}

		tailLen := min(len(chunk), 256)
		prevChunkTail = append(prevChunkTail[:0], chunk[len(chunk)-tailLen:]...)

		return nil
	}

	readRes, err := largebody.ProcessReplayChunks(largebody.ReplayChunkReaderConfig{
		Reader: &chunkHookReader{
			r: rc,
			onRead: func(chunk []byte) {
				currentChunk = chunk
				chunkStartOffset = totalBytesRead
				totalBytesRead += int64(len(chunk))
			},
		},
		MaxBytes:  bodyBytes,
		ChunkSize: 32 * 1024,
		Scanner:   scanner,
		Hasher:    hasher,
		OnChunk:   onChunk,
	})
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: json scanner feed: %w", err)
	}
	if bodyBytes <= 0 {
		bodyBytes = readRes.BytesRead
	}

	if unknownKey != "" {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: unsupported or unknown body key %q", unknownKey)
	}

	if !hasMessages {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: messages is required")
	}
	if !messagesIsArray {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: messages must be an array")
	}
	if arrayItemCount == 0 {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: messages is required")
	}

	if messagesArrayIsLarge {
		if arrayItemCount > frontendlimits.MaxMessages {
			return frontendpipe.ProofOutput{}, frontendlimits.Count("messages", arrayItemCount, frontendlimits.MaxMessages)
		}
		if largeArrayDeclineErr != nil {
			return frontendpipe.ProofOutput{}, largeArrayDeclineErr
		}
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

	model := strings.TrimSpace(modelBuf.String())
	if model == "" {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: model is required")
	}

	// 4. Parse metadata
	var metadata map[string]string
	if raw := fieldBufs["metadata"].Bytes(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: metadata: %w", err)
		}
	}
	if len(metadata) > 0 {
		if err := frontendlimits.Count("metadata", len(metadata), frontendlimits.MaxMetadata); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: %w", err)
		}
		if err := sessionwire.ValidateMetadata(metadata); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: %w", err)
		}
		// Requirement 14.2, 17.5: Reject body-carried LIP session metadata
		if sessionwire.HasSessionMetadata(metadata) {
			return frontendpipe.ProofOutput{}, largebody.ErrBodySessionMetadataRejected
		}
	}

	// 5. Resolve route selector precedence (Requirement 4.8, 17.6)
	sel := strings.TrimSpace(in.RouteSelector)
	if sel == "" && in.RouteFromBodyModel {
		sel = in.RoutePrefixes.InlineOrDefault(model, in.DefaultRouteSelector)
	}
	if sel == "" {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: route selector is required")
	}

	// 6. Parse tools, tool_choice, verbosity
	tools := []lipapi.ToolDef{}
	if fieldBufs["tools"].Len() > 0 {
		t, err := parseTools(fieldBufs["tools"].Bytes())
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: tools: %w", err)
		}
		tools = t
	}

	var toolChoice lipapi.ToolChoice
	if toolChoiceIsString {
		s := toolChoiceStrBuf.String()
		switch strings.TrimSpace(strings.ToLower(s)) {
		case "auto", "":
			toolChoice = lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}
		case "none":
			toolChoice = lipapi.ToolChoice{Mode: lipapi.ToolChoiceNone}
		case "required":
			toolChoice = lipapi.ToolChoice{Mode: lipapi.ToolChoiceAny}
		default:
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: unsupported tool_choice string %q", s)
		}
	} else if fieldBufs["tool_choice"].Len() > 0 {
		tc, err := parseToolChoice(fieldBufs["tool_choice"].Bytes())
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: tool_choice: %w", err)
		}
		toolChoice = tc
	} else {
		toolChoice = lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}
	}

	verbosity, err := lipapi.ParseVerbosityLevel(strings.TrimSpace(verbosityBuf.String()))
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: verbosity: %w", err)
	}

	// 7. Build extensions
	modelRaw, err := json.Marshal(model)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: marshal model: %w", err)
	}
	ext := map[string]json.RawMessage{extModelJSONKey: modelRaw}
	if b, err := json.Marshal(openrouterwire.FlavorChat); err == nil {
		ext[openrouterwire.ExtUpstreamFlavor] = b
	}
	if fieldBufs["stream_options"].Len() > 0 {
		raw := fieldBufs["stream_options"].Bytes()
		if jsonpresence.IsPresentNonNullJSON(raw) {
			ext[extStreamOptsJSONKey] = raw
		}
	}
	if in.Headers != nil {
		openrouterwire.CaptureHeaders(in.Headers, ext)
	}

	// 8. Extract session input
	// Semantic-fact budget enforcement point 2: sessionwire enforces MaxFactBytes
	sessIn, err := sessionwire.BuildSessionInput(in.Headers, metadata, sessionwire.SessionInputOptions{
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

	// 9. Build identity configuration
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
			Temperature:       temperature,
			TopP:              topP,
			MaxOutputTokens:   maxTokens,
			ParallelToolCalls: parallelTools,
			ReasoningEffort:   strings.TrimSpace(reasoningEffortBuf.String()),
			Verbosity:         verbosity,
		},
		Extensions: ext,
	}

	var digest largebody.IdentityDigest
	var turnShape largebody.ClientTurnShape

	if !messagesArrayIsLarge {
		var rawMsgs []json.RawMessage
		if err := json.Unmarshal(messagesArrayBuf.Bytes(), &rawMsgs); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: messages array: %w", err)
		}
		if len(rawMsgs) == 0 {
			return frontendpipe.ProofOutput{}, errors.New("openailegacy: messages is required")
		}
		if err := frontendlimits.Count("messages", len(rawMsgs), frontendlimits.MaxMessages); err != nil {
			return frontendpipe.ProofOutput{}, err
		}

		msgs := make([]lipapi.Message, 0, len(rawMsgs))
		for i, rawMsg := range rawMsgs {
			m, err := parseProfileMessage(rawMsg)
			if err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: messages[%d]: %w", i, err)
			}
			msgs = append(msgs, m)
		}

		// Semantic-fact budget enforcement point 3: ClientTurnShapeFromCall bounds turn facts
		ts, err := largebody.ClientTurnShapeFromCall(&lipapi.Call{
			Messages: msgs,
		}, frontendpipe.DefaultMaxSemanticFactBytes)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: turn shape: %w", err)
		}
		turnShape = ts

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
		d, err := idWriter.Digest()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: digest: %w", err)
		}
		digest = d
	} else {
		// Large chunked array messages (e.g. 20 MiB chunked messages): stream in Pass 2
		rc2, err := in.Source.Open()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: open replay source (pass 2): %w", err)
		}
		defer rc2.Close()

		idWriter, err := largebody.NewCallIdentityWriter(idCfg)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: identity writer: %w", err)
		}
		if err := idWriter.StartMessages(); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: start messages: %w", err)
		}

		var msgIndex int
		var currMsgWriter *largebody.MessageIdentityWriter
		var currTrimmer *largebody.TrimSpaceWriter
		var turnItems []largebody.ClientTurnItemShape
		var totalPartBytes int64
		var ordinal int64

		arrayStrResolver := func(sctx jsonshape.StringContext) (io.Writer, error) {
			if len(sctx.Path) >= 2 && sctx.Path[0] == "messages" && sctx.Key == "content" {
				if currMsgWriter != nil {
					_ = currMsgWriter.EndMessage()
				}
				role := lipapi.RoleUser
				if msgIndex < len(largeArrayRoles) {
					role = largeArrayRoles[msgIndex]
				}
				mw, err := idWriter.BeginMessage(role)
				if err != nil {
					return nil, err
				}
				currMsgWriter = mw
				pw, err := mw.BeginTextPart()
				if err != nil {
					return nil, err
				}
				currTrimmer = largebody.NewTrimSpaceWriter(pw)
				return currTrimmer, nil
			}
			return nil, nil
		}

		arrayHandler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
			if len(e.Path) >= 2 && e.Path[0] == "messages" && e.Type == jsonshape.EventString && e.Key == "content" {
				if currTrimmer != nil {
					_ = currTrimmer.Close()
				}
				role := lipapi.RoleUser
				if msgIndex < len(largeArrayRoles) {
					role = largeArrayRoles[msgIndex]
				}
				var partBytes int64
				if currTrimmer != nil {
					partBytes = currTrimmer.TrimmedBytes()
				}
				if partBytes == 0 {
					if role == lipapi.RoleAssistant {
						return errors.New("openailegacy: empty assistant message requires canonical drop")
					}
					return errors.New("openailegacy: message content string is empty")
				}
				turnItems = append(turnItems, largebody.ClientTurnItemShape{
					Kind:    lipapi.ItemKindMessage,
					Role:    role,
					Ordinal: ordinal,
					Parts: []largebody.ClientTurnPartShape{{
						Kind:         lipapi.ContentPartText,
						ContentBytes: partBytes,
					}},
				})
				totalPartBytes += partBytes
				ordinal++
				msgIndex++
			}
			return nil
		})

		arrayScanner := jsonshape.NewScanner(
			scanCtx,
			jsonshape.Limits{
				RejectDuplicateNames: true, // Item 6: set explicitly
				MaxBytes:             bodyBytes,
				MaxDepth:             128,
			},
			jsonshape.WithEventHandler(arrayHandler),
			jsonshape.WithStringWriterResolver(arrayStrResolver),
		)

		_, err = largebody.ProcessReplayChunks(largebody.ReplayChunkReaderConfig{
			Reader:    rc2,
			MaxBytes:  bodyBytes,
			ChunkSize: 32 * 1024,
			Scanner:   arrayScanner,
		})
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: streaming proof (pass 2): %w", err)
		}

		if currMsgWriter != nil {
			_ = currMsgWriter.EndMessage()
		}

		d, err := idWriter.Digest()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: digest: %w", err)
		}
		digest = d
		turnShape = largebody.ClientTurnShape{
			Items:             turnItems,
			TotalContentBytes: totalPartBytes,
		}
	}

	// 10. Compute source digest
	var sumArr [32]byte
	copy(sumArr[:], hasher.Sum(nil))
	sourceDigest := largebody.NewSourceDigest(sumArr)

	var maxOutTokens int64
	if maxTokens != nil {
		maxOutTokens = int64(*maxTokens)
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
		stream,
		sessIn,
		cancellationID,
	)

	proof := largebody.Proof{
		ProfileID:       ProfileID,
		Operation:       lipapi.OperationOpenAIChatCompletions,
		Delivery:        lipapi.DeliveryModeFromClientStream(stream),
		RouteSelector:   sel,
		ClientModel:     model,
		MaxOutputTokens: maxOutTokens,
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
