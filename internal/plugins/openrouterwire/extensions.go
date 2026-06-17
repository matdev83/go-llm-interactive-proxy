// Package openrouterwire provides shared extension keys and helpers for OpenRouter-specific
// data that flows from frontend decoders through lipapi.Call.Extensions to backend adapters.
// It lives at the adapter edge; core packages must not import it.
package openrouterwire

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

// Extension keys stored in lipapi.Call.Extensions by frontend decoders.
const (
	ExtProvider            = "openrouter.provider"
	ExtModels              = "openrouter.models"
	ExtRoute               = "openrouter.route"
	ExtPlugins             = "openrouter.plugins"
	ExtPrediction          = "openrouter.prediction"
	ExtDebug               = "openrouter.debug"
	ExtServiceTier         = "openrouter.service_tier"
	ExtSessionID           = "openrouter.session_id"
	ExtStopServerToolsWhen = "openrouter.stop_server_tools_when"
	ExtTrace               = "openrouter.trace"
	ExtInclude             = "openrouter.include"
	ExtUser                = "openrouter.user"
	ExtHTTPReferer         = "openrouter.http_referer"
	ExtTitle               = "openrouter.title"
	ExtCategories          = "openrouter.categories"
	ExtMetadataHeader      = "openrouter.metadata_header"
	ExtResponseFormat      = "openrouter.response_format"
	ExtReasoning           = "openrouter.reasoning"
	ExtUpstreamFlavor      = "openrouter.upstream_flavor"
)

// Upstream flavors for the backend adapter to select the correct endpoint.
const (
	FlavorChat      = "chat"
	FlavorResponses = "responses"
)

type passthroughBodyField struct {
	jsonKey string
	extKey  string
}

var bodyFieldPassthrough = []passthroughBodyField{
	{jsonKey: "provider", extKey: ExtProvider},
	{jsonKey: "models", extKey: ExtModels},
	{jsonKey: "route", extKey: ExtRoute},
	{jsonKey: "plugins", extKey: ExtPlugins},
	{jsonKey: "prediction", extKey: ExtPrediction},
	{jsonKey: "debug", extKey: ExtDebug},
	{jsonKey: "service_tier", extKey: ExtServiceTier},
	{jsonKey: "session_id", extKey: ExtSessionID},
	{jsonKey: "stop_server_tools_when", extKey: ExtStopServerToolsWhen},
	{jsonKey: "trace", extKey: ExtTrace},
	{jsonKey: "include", extKey: ExtInclude},
	{jsonKey: "user", extKey: ExtUser},
	{jsonKey: "response_format", extKey: ExtResponseFormat},
	{jsonKey: "reasoning", extKey: ExtReasoning},
}

var rawJSONNull = json.RawMessage("null")

// CaptureBodyFields reads OpenRouter-specific top-level fields from a decoded JSON body
// and stores them as json.RawMessage entries in extensions. Existing keys are overwritten.
func CaptureBodyFields(body map[string]json.RawMessage, ext map[string]json.RawMessage) {
	for _, field := range bodyFieldPassthrough {
		raw, ok := body[field.jsonKey]
		isMissing := !ok
		isEmpty := len(raw) == 0
		isJSONNull := bytes.Equal(raw, rawJSONNull)
		if isMissing || isEmpty || isJSONNull {
			continue
		}
		ext[field.extKey] = raw
	}
}

// CaptureHeaders reads OpenRouter-specific HTTP headers and stores them as quoted JSON strings
// in extensions.
func CaptureHeaders(h http.Header, ext map[string]json.RawMessage) {
	marshalString := func(v string) (json.RawMessage, bool) {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		return b, true
	}
	set := func(headerName, extKey string) {
		v := strings.TrimSpace(h.Get(headerName))
		if v == "" {
			return
		}
		b, ok := marshalString(v)
		if !ok {
			return
		}
		ext[extKey] = b
	}
	set("HTTP-Referer", ExtHTTPReferer)
	if v := strings.TrimSpace(h.Get("X-OpenRouter-Title")); v != "" {
		if b, ok := marshalString(v); ok {
			ext[ExtTitle] = b
		}
	} else {
		set("X-Title", ExtTitle)
	}
	set("X-OpenRouter-Categories", ExtCategories)
	set("X-OpenRouter-Metadata", ExtMetadataHeader)
}

// ExtraBodyExtPrefix is the extension key prefix used by OpenAI-compatible frontend adapters
// to pass NVIDIA-specific extra body fields through the canonical call.
const ExtraBodyExtPrefix = "nvidia.extra_body."

const (
	// MaxExtraBodyFields bounds provider-specific passthrough fields captured from one request body.
	MaxExtraBodyFields = 32
	// MaxExtraBodyFieldNameBytes bounds one provider-specific passthrough field name.
	MaxExtraBodyFieldNameBytes = 64
	// MaxExtraBodyFieldValueBytes bounds one provider-specific passthrough field value.
	MaxExtraBodyFieldValueBytes = 16 << 10
)

// CaptureExtraBodyFields stores unrecognized top-level JSON body fields as extensions
// under the ExtraBodyExtPrefix prefix. Fields present in knownKeys or in the
// OpenRouter bodyFieldPassthrough table are skipped. Null and empty values are skipped.
func CaptureExtraBodyFields(body map[string]json.RawMessage, ext map[string]json.RawMessage, knownKeys map[string]bool) {
	if ext == nil {
		return
	}
	captured := 0
	for key, raw := range body {
		if captured >= MaxExtraBodyFields {
			return
		}
		if knownKeys[key] {
			continue
		}
		if isPassthroughField(key) {
			continue
		}
		if !ValidExtraBodyFieldName(key) || !ExtraBodyValueWithinBounds(raw) {
			continue
		}
		ext[ExtraBodyExtPrefix+key] = raw
		captured++
	}
}

// ValidExtraBodyFieldName reports whether name is safe to pass to SDK JSON-set helpers as one top-level key.
func ValidExtraBodyFieldName(name string) bool {
	if name == "" || len(name) > MaxExtraBodyFieldNameBytes {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

// ExtraBodyValueWithinBounds reports whether raw is non-empty, non-null, and bounded for passthrough.
func ExtraBodyValueWithinBounds(raw json.RawMessage) bool {
	return len(raw) > 0 && len(raw) <= MaxExtraBodyFieldValueBytes && !bytes.Equal(raw, rawJSONNull)
}

func isPassthroughField(key string) bool {
	for _, f := range bodyFieldPassthrough {
		if f.jsonKey == key {
			return true
		}
	}
	return false
}

// GetString reads a JSON-quoted string from ext[key] or returns "".
func GetString(ext map[string]json.RawMessage, key string) string {
	raw, ok := ext[key]
	if !ok || len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// GetRaw returns the raw JSON value from ext[key] or nil.
func GetRaw(ext map[string]json.RawMessage, key string) json.RawMessage {
	raw, ok := ext[key]
	if !ok || len(raw) == 0 {
		return nil
	}
	return raw
}
