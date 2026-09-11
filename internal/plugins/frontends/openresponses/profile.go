package openresponses

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

type stringInspector struct {
	totalBytes       int64
	hasNonWhitespace bool
}

func newStringInspector() *stringInspector {
	return &stringInspector{}
}

func (si *stringInspector) Write(p []byte) (int, error) {
	si.totalBytes += int64(len(p))
	if !si.hasNonWhitespace {
		for _, b := range p {
			if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
				si.hasNonWhitespace = true
				break
			}
		}
	}
	return len(p), nil
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
// Uses the streaming proof pass without allocating or retaining the full request body.
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
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: open replay source: %w", err)
	}
	defer rc.Close()

	// 2. Pass 1: Streaming scan over replay bytes using jsonshape.Scanner + SHA-256 in fixed buffers (no ReadAll).
	tracker := jsonshape.NewTopLevelSpanTracker("model", "input")
	var unknownKey string

	var modelBuf bytes.Buffer
	var instructionsBuf bytes.Buffer
	inputInspector := newStringInspector()
	var toolChoiceStrBuf bytes.Buffer

	var stream bool
	var parallelTools *bool
	var temperature *float64
	var topP *float64
	var maxOut *int

	// store gate tracking
	var hasStore bool
	var storeIsFalse bool
	var storeVal string
	var storeBuf bytes.Buffer

	// For large array input role tracking and decline
	var arrayItemCount int
	var largeArrayRoles []lipapi.Role
	var largeArrayStatuses []lipapi.ItemStatus
	var largeArrayDeclineErr error
	var largeArrayHasID bool
	var currentItemRole lipapi.Role = lipapi.RoleUser
	var currentItemStatus lipapi.ItemStatus
	var currentItemHasContent bool
	var currentItemType string
	var itemRoleBuf bytes.Buffer
	var itemTypeBuf bytes.Buffer
	var itemStatusBuf bytes.Buffer

	// Composite field buffers (tools, tool_choice, text, reasoning, metadata)
	fieldBufs := make(map[string]*bytes.Buffer)
	for _, k := range []string{"tools", "tool_choice", "text", "reasoning", "metadata"} {
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
		return "", fmt.Errorf("openresponses: %s spanning chunk boundary requires canonical decode", e.Key)
	}

	var capturingKey string
	var capturingBuf *bytes.Buffer
	var capturingStart int64

	var capturingInputArray bool
	var inputArrayStart int64

	handler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
		if e.TopLevel && e.Type == jsonshape.EventKey {
			if !openResponsesKnownBodyKeys[e.Key] {
				unknownKey = e.Key
			}
			if e.Key == "input" {
				hasInput = true
			}
		}

		if e.TopLevel && e.Type != jsonshape.EventKey && e.Type != jsonshape.EventArrayEnd && e.Type != jsonshape.EventObjectEnd {
			switch e.Key {
			case "store":
				hasStore = true
				if e.Type == jsonshape.EventFalse {
					storeIsFalse = true
					storeVal = "false"
				} else if e.Type == jsonshape.EventTrue {
					storeVal = "true"
				} else if e.Type == jsonshape.EventNull {
					storeVal = "null"
				} else if e.Type == jsonshape.EventString {
					storeVal = fmt.Sprintf("%q", storeBuf.String())
				} else if e.Type == jsonshape.EventNumber {
					storeVal = "number"
				} else {
					storeVal = "invalid"
				}
			case "stream":
				if e.Type == jsonshape.EventTrue {
					stream = true
				} else if e.Type == jsonshape.EventFalse {
					stream = false
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: stream must be a boolean")
				}
			case "parallel_tool_calls":
				if e.Type == jsonshape.EventTrue {
					v := true
					parallelTools = &v
				} else if e.Type == jsonshape.EventFalse {
					v := false
					parallelTools = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: parallel_tool_calls must be a boolean")
				}
			case "temperature":
				if e.Type == jsonshape.EventNumber {
					numStr, err := extractNumberStr(e)
					if err != nil {
						return err
					}
					v, err := strconv.ParseFloat(numStr, 64)
					if err != nil {
						return fmt.Errorf("openresponses: temperature: %w", err)
					}
					temperature = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: temperature must be a number")
				}
			case "top_p":
				if e.Type == jsonshape.EventNumber {
					numStr, err := extractNumberStr(e)
					if err != nil {
						return err
					}
					v, err := strconv.ParseFloat(numStr, 64)
					if err != nil {
						return fmt.Errorf("openresponses: top_p: %w", err)
					}
					topP = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: top_p must be a number")
				}
			case "max_output_tokens":
				if e.Type == jsonshape.EventNumber {
					numStr, err := extractNumberStr(e)
					if err != nil {
						return err
					}
					if strings.Contains(numStr, ".") || strings.ContainsAny(numStr, "eE") {
						return errors.New("openresponses: max_output_tokens must be an integer")
					}
					v, err := strconv.Atoi(numStr)
					if err != nil {
						return errors.New("openresponses: max_output_tokens must be an integer")
					}
					maxOut = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: max_output_tokens must be an integer")
				}
			case "instructions":
				if e.Type != jsonshape.EventString && e.Type != jsonshape.EventNull {
					return errors.New("openresponses: instructions must be a string")
				}
			case "tools":
				if e.Type == jsonshape.EventArrayStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: tools must be an array")
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
					return errors.New("openresponses: tool_choice must be a string or object")
				}
			case "text":
				if e.Type == jsonshape.EventObjectStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: text must be an object")
				}
			case "reasoning":
				if e.Type == jsonshape.EventObjectStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: reasoning must be an object")
				}
			case "metadata":
				if e.Type == jsonshape.EventObjectStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: metadata must be an object")
				}
			case "model":
				if e.Type != jsonshape.EventString {
					return errors.New("openresponses: model must be a string")
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
					return errors.New("openresponses: input must be a string or array")
				}
			// Unsupported controls fail-closed when non-null
			case "previous_response_id":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: previous_response_id requires continuation state")
				}
			case "truncation":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: truncation control is not supported")
				}
			case "background":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: background control is not supported")
				}
			case "include":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: include control is not supported")
				}
			case "presence_penalty":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: presence_penalty control is not supported")
				}
			case "frequency_penalty":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: frequency_penalty control is not supported")
				}
			case "stream_options":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: stream_options control is not supported")
				}
			case "top_logprobs":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: top_logprobs control is not supported")
				}
			case "service_tier":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: service_tier control is not supported")
				}
			case "safety_identifier":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: safety_identifier control is not supported")
				}
			case "prompt_cache_key":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: prompt_cache_key control is not supported")
				}
			case "prompt_cache_retention":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: prompt_cache_retention control is not supported")
				}
			case "max_tool_calls":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: max_tool_calls control is not supported")
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

		// Array items inside input
		if !e.TopLevel && len(e.Path) >= 1 && e.Path[0] == "input" {
			if len(e.Path) == 1 {
				if e.Type == jsonshape.EventObjectStart {
					arrayItemCount++
					if arrayItemCount > frontendlimits.MaxMessages {
						return frontendlimits.Count("input", arrayItemCount, frontendlimits.MaxMessages)
					}
					currentItemRole = lipapi.RoleUser
					currentItemStatus = ""
					currentItemHasContent = false
					currentItemType = ""
				} else if e.Type == jsonshape.EventObjectEnd {
					if inputArrayIsLarge {
						if currentItemType != "" && currentItemType != "message" && largeArrayDeclineErr == nil {
							largeArrayDeclineErr = fmt.Errorf("openresponses: input item type %q requires canonical decode", currentItemType)
						}
						if !currentItemHasContent && largeArrayDeclineErr == nil {
							largeArrayDeclineErr = errors.New("openresponses: input item missing content requires canonical decode")
						}
					}
					largeArrayRoles = append(largeArrayRoles, currentItemRole)
					largeArrayStatuses = append(largeArrayStatuses, currentItemStatus)
					currentItemRole = lipapi.RoleUser
					currentItemStatus = ""
					itemRoleBuf.Reset()
					itemTypeBuf.Reset()
					itemStatusBuf.Reset()
				} else if e.Type != jsonshape.EventArrayEnd {
					return errors.New("openresponses: input array item must be an object")
				}
			} else if len(e.Path) == 2 {
				if e.Type == jsonshape.EventKey {
					if e.Key == "id" {
						largeArrayHasID = true
					} else if e.Key != "role" && e.Key != "content" && e.Key != "type" && e.Key != "status" && largeArrayDeclineErr == nil {
						largeArrayDeclineErr = fmt.Errorf("openresponses: unsupported input item field %q requires canonical decode", e.Key)
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
								largeArrayDeclineErr = fmt.Errorf("openresponses: unsupported role %q requires canonical decode", rStr)
							}
						}
					case "type":
						tStr := strings.TrimSpace(itemTypeBuf.String())
						currentItemType = tStr
						if tStr != "" && tStr != "message" && largeArrayDeclineErr == nil {
							largeArrayDeclineErr = fmt.Errorf("openresponses: unsupported input item type %q requires canonical decode", tStr)
						}
					case "status":
						sStr := strings.TrimSpace(itemStatusBuf.String())
						if sStr == "received" {
							sStr = "completed"
						}
						currentItemStatus = lipapi.ItemStatus(sStr)
					case "content":
						currentItemHasContent = true
					}
				}
			} else {
				if largeArrayDeclineErr == nil {
					largeArrayDeclineErr = errors.New("openresponses: complex nested input structure requires canonical decode")
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
			case "store":
				storeBuf.Reset()
				return &storeBuf, nil
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
			if sctx.Key == "status" {
				itemStatusBuf.Reset()
				return &itemStatusBuf, nil
			}
		}
		return nil, nil
	}

	hasher := sha256.New()
	scannerLimits := jsonshape.Limits{
		RejectDuplicateNames: true,
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
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: process replay chunks: %w", err)
	}
	if bodyBytes <= 0 {
		bodyBytes = readRes.BytesRead
	}

	if unknownKey != "" {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: unknown body key %q", unknownKey)
	}

	// 3. Store gate validation (Requirement 17.4)
	if !hasStore || storeVal == "null" {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: store is required and must be explicitly false")
	}
	if !storeIsFalse {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: store must be false, got %s", storeVal)
	}

	// 4. Check large array decline or limits
	if inputIsArray && inputArrayIsLarge {
		if arrayItemCount > frontendlimits.MaxMessages {
			return frontendpipe.ProofOutput{}, frontendlimits.Count("input", arrayItemCount, frontendlimits.MaxMessages)
		}
		if largeArrayHasID && largeArrayDeclineErr == nil {
			largeArrayDeclineErr = errors.New("openresponses: input item id in large array requires canonical decode")
		}
		if largeArrayDeclineErr != nil {
			return frontendpipe.ProofOutput{}, largeArrayDeclineErr
		}
	}

	// 5. Verify top-level "model" span
	modelSpanRaw, hasModel := tracker.Span("model")
	if !hasModel || modelSpanRaw.Length == 0 {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: model is required")
	}
	modelSpan := largebody.Span{Offset: modelSpanRaw.Offset, Length: modelSpanRaw.Length}
	rewrite, err := largebody.NewModelTokenRewrite(modelSpan)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: model rewrite: %w", err)
	}

	model := strings.TrimSpace(modelBuf.String())
	if model == "" {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: model is required")
	}

	// 6. Verify input presence and shape
	if !hasInput {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: input is required and cannot be empty")
	}
	if !inputIsString && !inputIsArray {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: input must be a string or array")
	}
	if inputIsString && inputInspector.totalBytes == 0 {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: input is required and cannot be empty")
	}
	if inputIsArray && arrayItemCount == 0 {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: input is required and cannot be empty")
	}

	// 7. Parse metadata
	var metadata map[string]string
	if raw := fieldBufs["metadata"].Bytes(); len(raw) > 0 {
		m, err := decodeMetadata(raw)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: metadata: %w", err)
		}
		metadata = m
	}
	if len(metadata) > 0 {
		if err := frontendlimits.Count("metadata", len(metadata), frontendlimits.MaxMetadata); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: %w", err)
		}
		if sessionwire.HasSessionMetadata(metadata) {
			return frontendpipe.ProofOutput{}, largebody.ErrBodySessionMetadataRejected
		}
	}

	// 8. Route selector precedence (Requirements 4.8, 17.6)
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

	// 9. Extract session input
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

	// 10. Parse tools and options
	canonicalTools, err := parseTools(fieldBufs["tools"].Bytes())
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: %w", err)
	}

	toolChoice, err := parseToolChoice(fieldBufs["tool_choice"].Bytes(), toolChoiceIsString, toolChoiceStrBuf.String())
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: %w", err)
	}

	responseMIME, err := parseTextConfig(fieldBufs["text"].Bytes())
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: %w", err)
	}

	var reasoningEffort string
	if fieldBufs["reasoning"].Len() > 0 {
		re, err := decodeReasoningControl(fieldBufs["reasoning"].Bytes())
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: %w", err)
		}
		reasoningEffort = re
	}

	// 11. Build identity configuration
	idCfg := largebody.CallIdentityConfig{
		Session: lipapi.SessionRef{
			AuthoritativeSessionID: sessIn.AuthoritativeSessionID,
			ClientSessionID:        sessIn.ClientSessionID,
			ResumeToken:            sessIn.ResumeToken.Reveal(),
			Metadata:               metadata,
		},
		SessionInput:  sessIn,
		RouteSelector: sel,
		Instructions:  nil, // In OpenResponses, instructions is placed in Items[0], NOT in Instructions
		Tools:         canonicalTools,
		ToolChoice:    toolChoice,
		Options: lipapi.GenerationOptions{
			Temperature:       temperature,
			TopP:              topP,
			MaxOutputTokens:   maxOut,
			ParallelToolCalls: parallelTools,
			ReasoningEffort:   reasoningEffort,
			ResponseMIMEType:  responseMIME,
		},
		Extensions: make(map[string]json.RawMessage),
	}

	var digest largebody.IdentityDigest
	var turnShape largebody.ClientTurnShape

	instructionText := instructionsBuf.String()
	hasInstructions := instructionsBuf.Len() > 0 && strings.TrimSpace(instructionText) != ""

	if inputIsString {
		// Pass 2: Stream input string directly through BeginMessageItem + BeginTextContentPart
		rc2, err := in.Source.Open()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: open replay source (pass 2): %w", err)
		}
		defer rc2.Close()

		idWriter, err := largebody.NewCallIdentityWriter(idCfg)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: identity writer: %w", err)
		}

		if err := idWriter.StartItems(); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: start items: %w", err)
		}

		var turnItems []largebody.ClientTurnItemShape
		var totalContentBytes int64
		var ordinal int64

		if hasInstructions {
			if err := idWriter.AddItem(lipapi.Item{
				Kind:    lipapi.ItemKindMessage,
				Status:  lipapi.ItemStatusCompleted,
				Role:    lipapi.RoleSystem,
				Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: instructionText}},
			}); err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: add instruction item: %w", err)
			}
			instLen := int64(len(instructionText))
			turnItems = append(turnItems, largebody.ClientTurnItemShape{
				Kind:    lipapi.ItemKindMessage,
				Role:    lipapi.RoleSystem,
				Ordinal: ordinal,
				Parts: []largebody.ClientTurnPartShape{{
					Kind:         lipapi.ContentPartText,
					ContentBytes: instLen,
				}},
			})
			totalContentBytes += instLen
			ordinal++
		}

		iw, err := idWriter.BeginMessageItem("", lipapi.ItemStatusCompleted, lipapi.RoleUser, "")
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: begin message item: %w", err)
		}

		pw, err := iw.BeginTextContentPart()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: begin text content part: %w", err)
		}

		strResolver2 := func(sctx jsonshape.StringContext) (io.Writer, error) {
			if sctx.TopLevel && sctx.Key == "input" {
				return pw, nil
			}
			return nil, nil
		}

		scanner2 := jsonshape.NewScanner(
			scanCtx,
			scannerLimits,
			jsonshape.WithStringWriterResolver(strResolver2),
		)

		if _, err := largebody.ProcessReplayChunks(largebody.ReplayChunkReaderConfig{
			Reader:    rc2,
			MaxBytes:  bodyBytes,
			ChunkSize: 32 * 1024,
			Scanner:   scanner2,
		}); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: streaming proof pass 2: %w", err)
		}

		if err := iw.EndItem(); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: end item: %w", err)
		}

		d, err := idWriter.Digest()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: digest: %w", err)
		}
		digest = d

		turnItems = append(turnItems, largebody.ClientTurnItemShape{
			Kind:    lipapi.ItemKindMessage,
			Role:    lipapi.RoleUser,
			Ordinal: ordinal,
			Parts: []largebody.ClientTurnPartShape{{
				Kind:         lipapi.ContentPartText,
				ContentBytes: inputInspector.totalBytes,
			}},
		})
		totalContentBytes += inputInspector.totalBytes

		turnShape = largebody.ClientTurnShape{
			Items:             turnItems,
			TotalContentBytes: totalContentBytes,
		}
	} else if !inputArrayIsLarge {
		// Small array input: parse items into lipapi.Item using canonical proto.DecodeItem
		var wireItems []proto.WireItem
		if err := json.Unmarshal(inputArrayBuf.Bytes(), &wireItems); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: invalid item array input: %w", err)
		}
		if err := frontendlimits.Count("input", len(wireItems), frontendlimits.MaxMessages); err != nil {
			return frontendpipe.ProofOutput{}, err
		}

		continuationRefs := 0
		for _, w := range wireItems {
			if w.Type == "item_reference" {
				continuationRefs++
			}
		}
		if err := proto.ValidateContinuationRefCount(continuationRefs, proto.DefaultLimits()); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: %w", err)
		}

		var canonicalItems []lipapi.Item
		if hasInstructions {
			canonicalItems = append(canonicalItems, lipapi.Item{
				Kind:    lipapi.ItemKindMessage,
				Status:  lipapi.ItemStatusCompleted,
				Role:    lipapi.RoleSystem,
				Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: instructionText}},
			})
		}
		for i, w := range wireItems {
			item, err := proto.DecodeItem(w, proto.DefaultLimits())
			if err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: item[%d]: %w", i, err)
			}
			canonicalItems = append(canonicalItems, item)
		}

		callForValidation := lipapi.Call{
			Items:      canonicalItems,
			Tools:      canonicalTools,
			ToolChoice: toolChoice,
			Options:    idCfg.Options,
		}
		if err := callForValidation.Validate(); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: canonical call validation failed: %w", err)
		}

		callForShape := lipapi.Call{
			Items: canonicalItems,
		}
		ts, err := largebody.ClientTurnShapeFromCall(&callForShape, frontendpipe.DefaultMaxSemanticFactBytes)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: turn shape: %w", err)
		}
		turnShape = ts

		idWriter, err := largebody.NewCallIdentityWriter(idCfg)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: identity writer: %w", err)
		}
		if err := idWriter.StartItems(); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: start items: %w", err)
		}
		for _, item := range canonicalItems {
			if err := idWriter.AddItem(item); err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: add item: %w", err)
			}
		}
		d, err := idWriter.Digest()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: digest: %w", err)
		}
		digest = d
	} else {
		// Large chunked array input: stream in Pass 2
		rc2, err := in.Source.Open()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: open replay source (pass 2): %w", err)
		}
		defer rc2.Close()

		idWriter, err := largebody.NewCallIdentityWriter(idCfg)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: identity writer: %w", err)
		}
		if err := idWriter.StartItems(); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: start items: %w", err)
		}

		var turnItems []largebody.ClientTurnItemShape
		var totalContentBytes int64
		var ordinal int64

		if hasInstructions {
			if err := idWriter.AddItem(lipapi.Item{
				Kind:    lipapi.ItemKindMessage,
				Status:  lipapi.ItemStatusCompleted,
				Role:    lipapi.RoleSystem,
				Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: instructionText}},
			}); err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: add instruction item: %w", err)
			}
			instLen := int64(len(instructionText))
			turnItems = append(turnItems, largebody.ClientTurnItemShape{
				Kind:    lipapi.ItemKindMessage,
				Role:    lipapi.RoleSystem,
				Ordinal: ordinal,
				Parts: []largebody.ClientTurnPartShape{{
					Kind:         lipapi.ContentPartText,
					ContentBytes: instLen,
				}},
			})
			totalContentBytes += instLen
			ordinal++
		}

		var itemIndex int
		var currItemWriter *largebody.ItemIdentityWriter
		var currPartBytes int64

		arrayStrResolver := func(sctx jsonshape.StringContext) (io.Writer, error) {
			if len(sctx.Path) >= 2 && sctx.Path[0] == "input" && sctx.Key == "content" {
				if currItemWriter != nil {
					_ = currItemWriter.EndItem()
				}
				role := lipapi.RoleUser
				if itemIndex < len(largeArrayRoles) {
					role = largeArrayRoles[itemIndex]
				}
				status := lipapi.ItemStatus("")
				if itemIndex < len(largeArrayStatuses) {
					status = largeArrayStatuses[itemIndex]
				}
				iw, err := idWriter.BeginMessageItem("", status, role, "")
				if err != nil {
					return nil, err
				}
				currItemWriter = iw
				pw, err := iw.BeginTextContentPart()
				if err != nil {
					return nil, err
				}
				currPartBytes = 0
				return &byteCountWriter{inner: pw, count: &currPartBytes}, nil
			}
			return nil, nil
		}

		arrayHandler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
			if len(e.Path) >= 2 && e.Path[0] == "input" && e.Type == jsonshape.EventString && e.Key == "content" {
				role := lipapi.RoleUser
				if itemIndex < len(largeArrayRoles) {
					role = largeArrayRoles[itemIndex]
				}
				turnItems = append(turnItems, largebody.ClientTurnItemShape{
					Kind:    lipapi.ItemKindMessage,
					Role:    role,
					Ordinal: ordinal,
					Parts: []largebody.ClientTurnPartShape{{
						Kind:         lipapi.ContentPartText,
						ContentBytes: currPartBytes,
					}},
				})
				totalContentBytes += currPartBytes
				ordinal++
				itemIndex++
			}
			return nil
		})

		arrayScanner := jsonshape.NewScanner(
			scanCtx,
			scannerLimits,
			jsonshape.WithEventHandler(arrayHandler),
			jsonshape.WithStringWriterResolver(arrayStrResolver),
		)

		if _, err := largebody.ProcessReplayChunks(largebody.ReplayChunkReaderConfig{
			Reader:    rc2,
			MaxBytes:  bodyBytes,
			ChunkSize: 32 * 1024,
			Scanner:   arrayScanner,
		}); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: array streaming proof: %w", err)
		}

		if currItemWriter != nil {
			_ = currItemWriter.EndItem()
		}
		d, err := idWriter.Digest()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: digest: %w", err)
		}
		digest = d

		turnShape = largebody.ClientTurnShape{
			Items:             turnItems,
			TotalContentBytes: totalContentBytes,
		}
	}

	// 12. Compute source digest
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

	deliveryMode := lipapi.DeliveryModeFromClientStream(stream)

	proof := largebody.Proof{
		ProfileID:       ProfileID,
		Operation:       lipapi.OperationOpenResponsesCreate,
		Delivery:        deliveryMode,
		RouteSelector:   sel,
		ClientModel:     model,
		MaxOutputTokens: maxTokens,
		Facts: largebody.ProtocolFacts{
			RequirementsID: ProfileID,
			ControlCount:   int64(len(canonicalTools)),
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

func parseTools(raw []byte) ([]lipapi.ToolDef, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var wireTools []struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	}
	if err := json.Unmarshal(trimmed, &wireTools); err != nil {
		return nil, errors.New("tools must be an array")
	}
	canonicalTools := make([]lipapi.ToolDef, 0, len(wireTools))
	for i, wt := range wireTools {
		if wt.Type != "function" {
			return nil, fmt.Errorf("tool[%d] has unsupported type %q", i, wt.Type)
		}
		if wt.Name == "" {
			return nil, fmt.Errorf("tool[%d] missing name", i)
		}
		canonicalTools = append(canonicalTools, lipapi.ToolDef{
			Name:        wt.Name,
			Description: wt.Description,
			Parameters:  bytes.Clone(wt.Parameters),
		})
	}
	return canonicalTools, nil
}

func parseToolChoice(raw []byte, isString bool, strVal string) (lipapi.ToolChoice, error) {
	if isString {
		switch strVal {
		case "auto":
			return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}, nil
		case "none":
			return lipapi.ToolChoice{Mode: lipapi.ToolChoiceNone}, nil
		case "required":
			return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAny}, nil
		default:
			return lipapi.ToolChoice{}, fmt.Errorf("unknown string tool_choice %q", strVal)
		}
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return lipapi.ToolChoice{}, nil
	}
	var rawObj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &rawObj); err != nil {
		return lipapi.ToolChoice{}, errors.New("tool_choice must be a string or object")
	}
	typeVal := ""
	if t, ok := rawObj["type"]; ok {
		_ = json.Unmarshal(t, &typeVal)
	}
	if typeVal == "function" {
		name := ""
		if rawName, ok := rawObj["name"]; ok {
			_ = json.Unmarshal(rawName, &name)
		}
		if name == "" {
			if fn, ok := rawObj["function"]; ok {
				var funcObj proto.WireToolChoiceFunctionName
				if err := json.Unmarshal(fn, &funcObj); err != nil {
					return lipapi.ToolChoice{}, errors.New("invalid function tool_choice object")
				}
				name = funcObj.Name
			}
		}
		if name == "" {
			return lipapi.ToolChoice{}, errors.New("invalid function tool_choice object missing name")
		}
		return lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired, Name: name}, nil
	}
	if typeVal == "allowed_tools" {
		return decodeAllowedTools(rawObj)
	}
	return lipapi.ToolChoice{}, fmt.Errorf("unknown object tool_choice type %q", typeVal)
}

func decodeAllowedTools(rawObj map[string]json.RawMessage) (lipapi.ToolChoice, error) {
	mode := lipapi.ToolChoiceAuto
	if raw, ok := rawObj["mode"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return lipapi.ToolChoice{}, errors.New("invalid allowed_tools mode")
		}
		switch s {
		case "auto":
			mode = lipapi.ToolChoiceAuto
		case "none":
			mode = lipapi.ToolChoiceNone
		case "required":
			mode = lipapi.ToolChoiceAny
		default:
			return lipapi.ToolChoice{}, fmt.Errorf("unknown allowed_tools mode %q", s)
		}
	}

	rawTools, ok := rawObj["tools"]
	if !ok {
		return lipapi.ToolChoice{}, errors.New("allowed_tools requires a tools array")
	}
	var refs []proto.WireToolChoiceAllowedToolRef
	if err := json.Unmarshal(rawTools, &refs); err != nil {
		return lipapi.ToolChoice{}, errors.New("invalid allowed_tools tools array")
	}
	if len(refs) == 0 {
		return lipapi.ToolChoice{}, errors.New("allowed_tools tools must not be empty")
	}
	if len(refs) > lipapi.MaxAllowedToolRefs {
		return lipapi.ToolChoice{}, fmt.Errorf("allowed_tools tools array exceeds %d references", lipapi.MaxAllowedToolRefs)
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.Type != "function" {
			return lipapi.ToolChoice{}, fmt.Errorf("allowed_tools tool reference has unsupported type %q", ref.Type)
		}
		if ref.Name == "" || ref.Name != strings.TrimSpace(ref.Name) {
			return lipapi.ToolChoice{}, errors.New("allowed_tools tool reference requires a valid name")
		}
		names = append(names, ref.Name)
	}
	return lipapi.ToolChoice{Mode: mode, AllowedTools: names}, nil
}

func parseTextConfig(raw []byte) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	if !supportedTextFormat(raw) {
		return "", errors.New("unsupported text format")
	}
	var text struct {
		Format *struct {
			Type string `json:"type"`
		} `json:"format"`
	}
	if err := json.Unmarshal(trimmed, &text); err != nil {
		return "", errors.New("unsupported text format")
	}
	if text.Format == nil {
		return "", nil
	}
	switch text.Format.Type {
	case "text":
		return "text/plain", nil
	case "json_object":
		return "application/json", nil
	default:
		return "", errors.New("unsupported text format")
	}
}

func decodeReasoningControl(raw []byte) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
		return "", errors.New("reasoning must be an object")
	}
	for key := range fields {
		if key != "effort" {
			return "", fmt.Errorf("reasoning field %q is unsupported", key)
		}
	}
	rawEffort, ok := fields["effort"]
	if !ok || len(rawEffort) == 0 || bytes.Equal(bytes.TrimSpace(rawEffort), []byte("null")) {
		return "", nil
	}
	var effort string
	if err := json.Unmarshal(rawEffort, &effort); err != nil {
		return "", errors.New("reasoning.effort must be a string")
	}
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return "", errors.New("reasoning.effort must not be empty")
	}
	switch effort {
	case "none", "low", "medium", "high", "xhigh":
		return effort, nil
	default:
		return "", fmt.Errorf("reasoning.effort %q is not supported", effort)
	}
}

var _ frontendpipe.FrontendProfile = (*Profile)(nil)
