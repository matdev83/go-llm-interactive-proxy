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
