package openairesponses

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

type stringInspector struct {
	trimmer          *largebody.TrimSpaceWriter
	totalBytes       int64
	hasNonWhitespace bool
}

func newStringInspector() *stringInspector {
	return &stringInspector{
		trimmer: largebody.NewTrimSpaceWriter(io.Discard),
	}
}

func (si *stringInspector) Write(p []byte) (int, error) {
	n, err := si.trimmer.Write(p)
	if si.trimmer.TrimmedBytes() > 0 {
		si.hasNonWhitespace = true
	}
	return n, err
}

func (si *stringInspector) Close() error {
	err := si.trimmer.Close()
	si.totalBytes = si.trimmer.TrimmedBytes()
	if si.totalBytes > 0 {
		si.hasNonWhitespace = true
	}
	return err
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

type byteCountWriter struct {
	inner io.Writer
	count *int64
}

func (w *byteCountWriter) Write(p []byte) (int, error) {
	n, err := w.inner.Write(p)
	if n > 0 && w.count != nil {
		*w.count += int64(n)
	}
	return n, err
}

func (w *byteCountWriter) Close() error {
	if c, ok := w.inner.(io.Closer); ok {
		return c.Close()
	}
	return nil
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

// CompileProof compiles protocol proof and response seeds from the
// captured request replay under decode admission (Requirements 4, 13, 14, 16, 17).
// Uses the streaming proof pass without allocating or retaining the full request body.
func (p *Profile) CompileProof(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
	// 1. Path verification
	if !strings.HasSuffix(in.URLPath, "/responses") && in.URLPath != "/responses" {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: unsupported url path")
	}

	if in.Source == nil {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: nil replay source")
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
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: open replay source: %w", err)
	}
	defer rc.Close()

	// 2. Pass 1: Streaming scan over replay bytes using jsonshape.Scanner + SHA-256 in fixed buffers (no ReadAll).
	// Enforces:
	//   - RejectDuplicateNames (canonical-only if duplicate keys)
	//   - Track exact byte span for "model"
	//   - Detect unknown top-level keys (canonical-only if unknown)
	//   - Detect unsupported control keys: "store", "previous_response_id", "truncation" (canonical-only)
	//   - Extract small envelope fields (model, stream, instructions, tools, etc.) bounded by DefaultMaxSemanticFactBytes
	//   - Detect input presence and shape (string vs array) without materializing large input in memory
	tracker := jsonshape.NewTopLevelSpanTracker("model", "input")
	var unknownKey string
	var hasUnsupportedControl bool

	var modelBuf bytes.Buffer
	var instructionsBuf bytes.Buffer
	inputInspector := newStringInspector()
	var toolChoiceStrBuf bytes.Buffer

	var stream bool
	var parallelTools *bool
	var temperature *float64
	var topP *float64
	var maxOut *int

	// For large array input role tracking and decline (Item 2):
	var arrayItemCount int
	var largeArrayRoles []lipapi.Role
	var largeArrayDeclineErr error
	var currentItemRole lipapi.Role = lipapi.RoleUser
	var currentItemHasContent bool
	var itemRoleBuf bytes.Buffer
	var itemTypeBuf bytes.Buffer

	// Composite field buffers (tools, text, metadata, tool_choice object, input array)
	// Semantic-fact budget enforcement point 1: Bounded by DefaultMaxSemanticFactBytes (256 KiB).
	fieldBufs := make(map[string]*bytes.Buffer)
	for _, k := range []string{"tools", "tool_choice", "text", "metadata"} {
		fieldBufs[k] = &bytes.Buffer{}
	}
	var inputArrayBuf bytes.Buffer
	inputArrayIsLarge := bodyBytes > 2*frontendpipe.DefaultMaxSemanticFactBytes
	var totalFactBytes int64

	var hasInput bool
	var inputIsString bool
	var inputIsArray bool
	var toolChoiceIsString bool

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
		return "", fmt.Errorf("openairesponses: %s spanning chunk boundary requires canonical decode", e.Key)
	}

	var capturingKey string
	var capturingBuf *bytes.Buffer
	var capturingStart int64

	var capturingInputArray bool
	var inputArrayStart int64

	handler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
		if e.TopLevel && e.Type == jsonshape.EventKey {
			if !responsesKnownBodyKeys[e.Key] {
				unknownKey = e.Key
			}
			if e.Key == "store" || e.Key == "previous_response_id" || e.Key == "truncation" {
				hasUnsupportedControl = true
			}
			if e.Key == "input" {
				hasInput = true
			}
		}

		if e.TopLevel && e.Type != jsonshape.EventKey && e.Type != jsonshape.EventArrayEnd && e.Type != jsonshape.EventObjectEnd {
			switch e.Key {
			case "instructions":
				if e.Type != jsonshape.EventString && e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: instructions must be a JSON string in this adapter")
				}
			case "tools":
				if e.Type == jsonshape.EventArrayStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: tools must be an array")
				}
			case "text":
				if e.Type == jsonshape.EventObjectStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: text must be an object")
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
					return errors.New("openairesponses: tool_choice must be a string or object")
				}
			case "stream":
				if e.Type == jsonshape.EventTrue {
					stream = true
				} else if e.Type == jsonshape.EventFalse {
					stream = false
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: stream must be a boolean")
				}
			case "parallel_tool_calls":
				if e.Type == jsonshape.EventTrue {
					v := true
					parallelTools = &v
				} else if e.Type == jsonshape.EventFalse {
					v := false
					parallelTools = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: parallel_tool_calls must be a boolean")
				}
			case "temperature":
				if e.Type == jsonshape.EventNumber {
					numStr, err := extractNumberStr(e)
					if err != nil {
						return err
					}
					v, err := strconv.ParseFloat(numStr, 64)
					if err != nil {
						return fmt.Errorf("openairesponses: temperature: %w", err)
					}
					temperature = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: temperature must be a number")
				}
			case "top_p":
				if e.Type == jsonshape.EventNumber {
					numStr, err := extractNumberStr(e)
					if err != nil {
						return err
					}
					v, err := strconv.ParseFloat(numStr, 64)
					if err != nil {
						return fmt.Errorf("openairesponses: top_p: %w", err)
					}
					topP = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: top_p must be a number")
				}
			case "max_output_tokens":
				if e.Type == jsonshape.EventNumber {
					numStr, err := extractNumberStr(e)
					if err != nil {
						return err
					}
					v, err := strconv.Atoi(numStr)
					if err != nil {
						return fmt.Errorf("openairesponses: max_output_tokens must be an integer: %w", err)
					}
					maxOut = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: max_output_tokens must be an integer")
				}
			case "model":
				if e.Type != jsonshape.EventString {
					return errors.New("openairesponses: model must be a string")
				}
			case "input":
				if e.Type == jsonshape.EventString {
					inputIsString = true
				} else if e.Type == jsonshape.EventArrayStart {
					inputIsArray = true
					capturingInputArray = true
					inputArrayStart = e.Offset
					inputArrayBuf.Reset()
				} else {
					return errors.New("openairesponses: input must be a string or array")
				}
			case "metadata":
				if e.Type == jsonshape.EventObjectStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: metadata must be an object")
				}
			}
		}

		if e.TopLevel {
			if e.Type == jsonshape.EventArrayEnd {
				if e.Key == "input" {
					if capturingInputArray && !inputArrayIsLarge {
						start := max(chunkStartOffset, inputArrayStart)
						end := e.Offset + e.Length
						if start < end && end <= chunkStartOffset+int64(len(currentChunk)) {
							slice := currentChunk[start-chunkStartOffset : end-chunkStartOffset]
							if int64(inputArrayBuf.Len()+len(slice)) > frontendpipe.DefaultMaxSemanticFactBytes {
								inputArrayIsLarge = true
								inputArrayBuf.Reset()
							} else {
								inputArrayBuf.Write(slice)
							}
						}
					}
					capturingInputArray = false
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

		// Item 2: Inspect array items inside input
		if !e.TopLevel && len(e.Path) >= 1 && e.Path[0] == "input" {
			if len(e.Path) == 1 {
				if e.Type == jsonshape.EventObjectStart {
					arrayItemCount++
					if arrayItemCount > frontendlimits.MaxMessages {
						return frontendlimits.Count("input", arrayItemCount, frontendlimits.MaxMessages)
					}
					currentItemRole = lipapi.RoleUser
					currentItemHasContent = false
				} else if e.Type == jsonshape.EventObjectEnd {
					if !currentItemHasContent && largeArrayDeclineErr == nil {
						largeArrayDeclineErr = errors.New("openairesponses: input item missing content")
					}
					largeArrayRoles = append(largeArrayRoles, currentItemRole)
					currentItemRole = lipapi.RoleUser
					itemRoleBuf.Reset()
					itemTypeBuf.Reset()
				} else {
					if largeArrayDeclineErr == nil {
						largeArrayDeclineErr = errors.New("openairesponses: input array item must be an object")
					}
				}
			} else if len(e.Path) == 2 {
				if e.Type == jsonshape.EventKey {
					if e.Key != "role" && e.Key != "content" && e.Key != "type" && largeArrayDeclineErr == nil {
						largeArrayDeclineErr = fmt.Errorf("openairesponses: unsupported input item field %q requires canonical decode", e.Key)
					}
				} else if e.Type == jsonshape.EventString {
					switch e.Key {
					case "role":
						rStr := strings.TrimSpace(itemRoleBuf.String())
						switch rStr {
						case "user":
							currentItemRole = lipapi.RoleUser
						case "assistant":
							currentItemRole = lipapi.RoleAssistant
						case "system":
							currentItemRole = lipapi.RoleSystem
						default:
							if largeArrayDeclineErr == nil {
								largeArrayDeclineErr = fmt.Errorf("openairesponses: unsupported role %q requires canonical decode", rStr)
							}
						}
					case "type":
						tStr := strings.TrimSpace(itemTypeBuf.String())
						if tStr != "" && tStr != "message" && largeArrayDeclineErr == nil {
							largeArrayDeclineErr = fmt.Errorf("openairesponses: unsupported input item type %q requires canonical decode", tStr)
						}
					case "content":
						currentItemHasContent = true
					}
				} else {
					if largeArrayDeclineErr == nil {
						largeArrayDeclineErr = fmt.Errorf("openairesponses: input item field %q must be a string", e.Key)
					}
				}
			} else {
				if largeArrayDeclineErr == nil {
					largeArrayDeclineErr = errors.New("openairesponses: complex nested input structure requires canonical decode")
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
			case "instructions":
				return &budgetBoundedWriter{buf: &instructionsBuf, total: &totalFactBytes, maxBudget: frontendpipe.DefaultMaxSemanticFactBytes}, nil
			case "tool_choice":
				return &budgetBoundedWriter{buf: &toolChoiceStrBuf, total: &totalFactBytes, maxBudget: frontendpipe.DefaultMaxSemanticFactBytes}, nil
			case "input":
				inputIsString = true
				return inputInspector, nil
			}
		}
		if len(sctx.Path) >= 1 && sctx.Path[0] == "input" {
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
		RejectDuplicateNames: true, // Carry-over nit 1: set explicitly to prevent silent relaxation
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

		if capturingInputArray && !inputArrayIsLarge {
			start := max(chunkStartOffset, inputArrayStart)
			if start < chunkEndOffset {
				slice := chunk[start-chunkStartOffset:]
				if int64(inputArrayBuf.Len()+len(slice)) > frontendpipe.DefaultMaxSemanticFactBytes {
					inputArrayIsLarge = true
					inputArrayBuf.Reset()
				} else {
					inputArrayBuf.Write(slice)
					inputArrayStart = chunkEndOffset
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
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: process replay chunks: %w", err)
	}
	if bodyBytes <= 0 {
		bodyBytes = readRes.BytesRead
	}

	if unknownKey != "" {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: unknown body key %q", unknownKey)
	}
	if hasUnsupportedControl {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: unsupported control key (store/previous_response_id/truncation)")
	}

	if inputIsArray && inputArrayIsLarge {
		if arrayItemCount > frontendlimits.MaxMessages {
			return frontendpipe.ProofOutput{}, frontendlimits.Count("input", arrayItemCount, frontendlimits.MaxMessages)
		}
		if largeArrayDeclineErr != nil {
			return frontendpipe.ProofOutput{}, largeArrayDeclineErr
		}
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

	model := strings.TrimSpace(modelBuf.String())
	if model == "" {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: model is required")
	}

	// 4. Verify input presence and shape
	if !hasInput {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: input is required")
	}
	if !inputIsString && !inputIsArray {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: input must be a string or array")
	}
	if inputIsString && !inputInspector.hasNonWhitespace {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: input string is empty")
	}

	// 5. Parse metadata
	var metadata map[string]string
	if raw := fieldBufs["metadata"].Bytes(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: metadata: %w", err)
		}
	}
	if len(metadata) > 0 {
		if err := frontendlimits.Count("metadata", len(metadata), frontendlimits.MaxMetadata); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: %w", err)
		}
		// Requirement 14.2, 17.5: Reject body-carried LIP session metadata
		if sessionwire.HasSessionMetadata(metadata) {
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
	instructions := []lipapi.Message{}
	if instructionsBuf.Len() > 0 {
		s := strings.TrimSpace(instructionsBuf.String())
		if s != "" {
			instructions = []lipapi.Message{{
				Role:  lipapi.RoleSystem,
				Parts: []lipapi.Part{lipapi.TextPart(s)},
			}}
		}
	}

	// 8. Parse tools and options
	tools, err := parseTools(fieldBufs["tools"].Bytes())
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: tools: %w", err)
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
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: unsupported tool_choice string %q", s)
		}
	} else if fieldBufs["tool_choice"].Len() > 0 {
		tc, err := parseToolChoice(fieldBufs["tool_choice"].Bytes())
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: tool_choice: %w", err)
		}
		toolChoice = tc
	} else {
		toolChoice = lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}
	}

	verbosity, remainingText, err := parseTextConfig(fieldBufs["text"].Bytes())
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: text: %w", err)
	}

	// 9. Build extensions
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

	// 10. Extract session input
	// Semantic-fact budget enforcement point 2: sessionwire enforces MaxFactBytes
	sessIn, err := sessionwire.BuildSessionInput(in.Headers, metadata, sessionwire.SessionInputOptions{
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

	// 11. Build identity configuration
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
			Temperature:       temperature,
			TopP:              topP,
			MaxOutputTokens:   maxOut,
			ParallelToolCalls: parallelTools,
			Verbosity:         verbosity,
		},
		Extensions: ext,
	}

	var digest largebody.IdentityDigest
	var turnShape largebody.ClientTurnShape

	if inputIsString {
		// Pass 2: Stream input string directly through CompileStreamingProof without full body allocation.
		rc2, err := in.Source.Open()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: open replay source (pass 2): %w", err)
		}
		defer rc2.Close()

		streamingCfg := largebody.StreamingProofConfig{
			Reader:             rc2,
			MaxBytes:           bodyBytes,
			ChunkSize:          32 * 1024,
			CallIdentityConfig: idCfg,
			ScannerLimits: jsonshape.Limits{
				RejectDuplicateNames: true, // Carry-over nit 1: set explicitly to prevent silent relaxation
				MaxBytes:             bodyBytes,
				MaxDepth:             128,
			},
		}
		streamRes, err := largebody.CompileStreamingProof(scanCtx, streamingCfg)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: streaming proof: %w", err)
		}
		digest = streamRes.Digest

		// Build turn shape directly from instructions and counted input string bytes
		turnShape = largebody.ClientTurnShape{
			Items: make([]largebody.ClientTurnItemShape, 0, len(instructions)+1),
		}
		var ordinal int64
		for _, inst := range instructions {
			var instBytes int64
			for _, p := range inst.Parts {
				instBytes += int64(len(p.Text))
			}
			turnShape.Items = append(turnShape.Items, largebody.ClientTurnItemShape{
				Kind:    lipapi.ItemKindMessage,
				Role:    lipapi.RoleSystem,
				Ordinal: ordinal,
				Parts: []largebody.ClientTurnPartShape{{
					Kind:         lipapi.ContentPartText,
					ContentBytes: instBytes,
				}},
			})
			turnShape.TotalContentBytes += instBytes
			ordinal++
		}
		turnShape.Items = append(turnShape.Items, largebody.ClientTurnItemShape{
			Kind:    lipapi.ItemKindMessage,
			Role:    lipapi.RoleUser,
			Ordinal: ordinal,
			Parts: []largebody.ClientTurnPartShape{{
				Kind:         lipapi.ContentPartText,
				ContentBytes: inputInspector.totalBytes,
			}},
		})
		turnShape.TotalContentBytes += inputInspector.totalBytes
	} else if !inputArrayIsLarge {
		// Small array input: parse items into messages using canonical parseInputItem
		var items []json.RawMessage
		if err := json.Unmarshal(inputArrayBuf.Bytes(), &items); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: input array: %w", err)
		}
		if err := frontendlimits.Count("input", len(items), frontendlimits.MaxMessages); err != nil {
			return frontendpipe.ProofOutput{}, err
		}
		msgs := make([]lipapi.Message, 0, len(items))
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

		// Semantic-fact budget enforcement point 3: ClientTurnShapeFromCall bounds turn facts
		ts, err := largebody.ClientTurnShapeFromCall(&lipapi.Call{
			Instructions: instructions,
			Messages:     msgs,
		}, frontendpipe.DefaultMaxSemanticFactBytes)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: turn shape: %w", err)
		}
		turnShape = ts

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
		d, err := idWriter.Digest()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: digest: %w", err)
		}
		digest = d
	} else {
		// Large chunked array input (e.g. 20 MiB chunked messages): stream in Pass 2
		rc2, err := in.Source.Open()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: open replay source (pass 2): %w", err)
		}
		defer rc2.Close()

		idWriter, err := largebody.NewCallIdentityWriter(idCfg)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: identity writer: %w", err)
		}
		if err := idWriter.StartMessages(); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: start messages: %w", err)
		}

		var msgIndex int
		var currMsgWriter *largebody.MessageIdentityWriter
		var currTrimmer *largebody.TrimSpaceWriter
		var turnItems []largebody.ClientTurnItemShape
		var totalPartBytes int64
		var ordinal int64 = int64(len(instructions))

		arrayStrResolver := func(sctx jsonshape.StringContext) (io.Writer, error) {
			if len(sctx.Path) >= 2 && sctx.Path[0] == "input" && sctx.Key == "content" {
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
			if len(e.Path) >= 2 && e.Path[0] == "input" && e.Type == jsonshape.EventString && e.Key == "content" {
				role := lipapi.RoleUser
				if msgIndex < len(largeArrayRoles) {
					role = largeArrayRoles[msgIndex]
				}
				var partBytes int64
				if currTrimmer != nil {
					partBytes = currTrimmer.TrimmedBytes()
				}
				if partBytes == 0 {
					return errors.New("openairesponses: message content string is empty")
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
				RejectDuplicateNames: true, // Carry-over nit 1: set explicitly
				MaxBytes:             bodyBytes,
				MaxDepth:             128,
			},
			jsonshape.WithEventHandler(arrayHandler),
			jsonshape.WithStringWriterResolver(arrayStrResolver),
		)

		if _, err := largebody.ProcessReplayChunks(largebody.ReplayChunkReaderConfig{
			Reader:    rc2,
			MaxBytes:  bodyBytes,
			ChunkSize: 32 * 1024,
			Scanner:   arrayScanner,
		}); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: array streaming proof: %w", err)
		}

		if currMsgWriter != nil {
			_ = currMsgWriter.EndMessage()
		}
		d, err := idWriter.Digest()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: digest: %w", err)
		}
		digest = d

		var instItems []largebody.ClientTurnItemShape
		var instBytesTotal int64
		var instOrd int64
		for _, inst := range instructions {
			var b int64
			for _, p := range inst.Parts {
				b += int64(len(p.Text))
			}
			instItems = append(instItems, largebody.ClientTurnItemShape{
				Kind:    lipapi.ItemKindMessage,
				Role:    lipapi.RoleSystem,
				Ordinal: instOrd,
				Parts: []largebody.ClientTurnPartShape{{
					Kind:         lipapi.ContentPartText,
					ContentBytes: b,
				}},
			})
			instBytesTotal += b
			instOrd++
		}
		turnShape = largebody.ClientTurnShape{
			Items:             append(instItems, turnItems...),
			TotalContentBytes: instBytesTotal + totalPartBytes,
		}
	}

	// 12. Compute source digest from Pass 1 full replay hasher
	var sumArr [32]byte
	copy(sumArr[:], hasher.Sum(nil))
	sourceDigest := largebody.NewSourceDigest(sumArr)

	var maxTokens int64
	if maxOut != nil {
		maxTokens = int64(*maxOut)
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
		Operation:       lipapi.OperationOpenAIResponses,
		Delivery:        lipapi.DeliveryModeFromClientStream(stream),
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

	// Semantic-fact budget enforcement point 4: proofOut validates Turn and Facts budgets
	if err := proofOut.Validate(frontendpipe.DefaultMaxSemanticFactBytes); err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: validate proof output: %w", err)
	}

	return proofOut, nil
}

var _ frontendpipe.FrontendProfile = (*Profile)(nil)
