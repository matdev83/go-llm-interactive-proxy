package openresponses

import (
	"encoding/json"
	"time"
)

// UsageStats carries token count statistics for building ResponseResource / CompactResource.
type UsageStats struct {
	InputTokens     int
	CachedTokens    int
	OutputTokens    int
	ReasoningTokens int
	TotalTokens     int
}

// WireResponseParam represents the wire request body for OpenResponses POST /responses.
type WireResponseParam struct {
	Model                *string                    `json:"model,omitempty"`
	Input                json.RawMessage            `json:"input"`
	Instructions         *string                    `json:"instructions,omitempty"`
	Tools                []WireTool                 `json:"tools,omitempty"`
	ToolChoice           json.RawMessage            `json:"tool_choice,omitempty"`
	ParallelToolCalls    *bool                      `json:"parallel_tool_calls,omitempty"`
	Temperature          *float64                   `json:"temperature,omitempty"`
	TopP                 *float64                   `json:"top_p,omitempty"`
	MaxOutputTokens      *int                       `json:"max_output_tokens,omitempty"`
	MaxToolCalls         *int                       `json:"max_tool_calls,omitempty"`
	Truncation           *string                    `json:"truncation,omitempty"`
	Text                 json.RawMessage            `json:"text,omitempty"`
	Reasoning            json.RawMessage            `json:"reasoning,omitempty"`
	Store                *bool                      `json:"store,omitempty"`
	Background           *bool                      `json:"background,omitempty"`
	PreviousResponseID   *string                    `json:"previous_response_id,omitempty"`
	Metadata             json.RawMessage            `json:"metadata,omitempty"`
	ServiceTier          *string                    `json:"service_tier,omitempty"`
	SafetyIdentifier     *string                    `json:"safety_identifier,omitempty"`
	PromptCacheKey       *string                    `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention *string                    `json:"prompt_cache_retention,omitempty"`
	ExtraFields          map[string]json.RawMessage `json:"-"`
}

// WireItem represents a single item on the wire in OpenResponses.
type WireItem struct {
	ID             string          `json:"id,omitempty"`
	Type           string          `json:"type"`
	Status         string          `json:"status,omitempty"`
	Role           string          `json:"role,omitempty"`
	Phase          string          `json:"phase,omitempty"`
	Content        json.RawMessage `json:"content,omitempty"`
	CallID         string          `json:"call_id,omitempty"`
	Name           string          `json:"name,omitempty"`
	Arguments      json.RawMessage `json:"arguments,omitempty"`
	Output         json.RawMessage `json:"output,omitempty"`
	Reasoning      json.RawMessage `json:"reasoning,omitempty"`
	EncapsulatedID string          `json:"encapsulated_id,omitempty"`
	Dialect        string          `json:"dialect,omitempty"`
	Implementor    string          `json:"implementor,omitempty"`
	// EncryptedContent is the pinned-profile compaction blob carried on a
	// response.compaction output item.
	EncryptedContent string          `json:"encrypted_content,omitempty"`
	Opaque           json.RawMessage `json:"opaque,omitempty"`
	Namespace        string          `json:"namespace,omitempty"`
	Direction        string          `json:"direction,omitempty"`
	Data             json.RawMessage `json:"data,omitempty"`
}

// WireContentPart represents a content part inside a message or tool result on the wire.
type WireContentPart struct {
	Type         string          `json:"type"`
	Text         string          `json:"text"`
	ImageURL     json.RawMessage `json:"image_url,omitempty"`
	FileURL      json.RawMessage `json:"file_url,omitempty"`
	VideoURL     json.RawMessage `json:"video_url,omitempty"`
	Refusal      string          `json:"refusal,omitempty"`
	Reasoning    json.RawMessage `json:"reasoning,omitempty"`
	Summary      string          `json:"summary,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
	AssistantRef string          `json:"assistant_ref,omitempty"`
	Logprobs     json.RawMessage `json:"logprobs,omitempty"`
}

// WireTool represents a tool definition on the wire.
type WireTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// WireUsageInputDetails captures known input token detail counters.
type WireUsageInputDetails struct {
	CachedTokens int `json:"cached_tokens"`
	TextTokens   int `json:"text_tokens,omitempty"`
	AudioTokens  int `json:"audio_tokens,omitempty"`
	Images       int `json:"images,omitempty"`
}

// WireUsageOutputDetails captures known output token detail counters.
type WireUsageOutputDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
	TextTokens      int `json:"text_tokens,omitempty"`
	AudioTokens     int `json:"audio_tokens,omitempty"`
}

// WireUsage represents token usage statistics on the wire.
type WireUsage struct {
	InputTokens         int                    `json:"input_tokens"`
	InputTokensDetails  WireUsageInputDetails  `json:"input_tokens_details"`
	OutputTokens        int                    `json:"output_tokens"`
	OutputTokensDetails WireUsageOutputDetails `json:"output_tokens_details"`
	TotalTokens         int                    `json:"total_tokens"`
}

// WireToolChoiceFunction is the object form of a required tool choice naming one function.
type WireToolChoiceFunction struct {
	Function WireToolChoiceFunctionName `json:"function"`
	Type     string                     `json:"type"`
}

// WireToolChoiceFunctionName names the specific function for a required tool choice.
type WireToolChoiceFunctionName struct {
	Name string `json:"name"`
}

// WireResponseResource represents the complete OpenResponses response resource.
type WireResponseResource struct {
	ID                   string                 `json:"id"`
	Object               string                 `json:"object"`
	CreatedAt            int64                  `json:"created_at"`
	Status               string                 `json:"status"`
	CompletedAt          *int64                 `json:"completed_at"`
	Model                string                 `json:"model"`
	Output               []WireItem             `json:"output"`
	ParallelToolCalls    bool                   `json:"parallel_tool_calls"`
	Reasoning            json.RawMessage        `json:"reasoning"`
	Store                bool                   `json:"store"`
	Background           bool                   `json:"background"`
	Temperature          *float64               `json:"temperature"`
	Text                 map[string]interface{} `json:"text"`
	ToolChoice           json.RawMessage        `json:"tool_choice"`
	Tools                []WireTool             `json:"tools"`
	TopP                 *float64               `json:"top_p"`
	PresencePenalty      float64                `json:"presence_penalty"`
	FrequencyPenalty     float64                `json:"frequency_penalty"`
	TopLogprobs          int                    `json:"top_logprobs"`
	Truncation           string                 `json:"truncation"`
	Usage                WireUsage              `json:"usage"`
	Metadata             map[string]interface{} `json:"metadata"`
	ServiceTier          string                 `json:"service_tier"`
	MaxOutputTokens      *int                   `json:"max_output_tokens"`
	MaxToolCalls         *int                   `json:"max_tool_calls"`
	Instructions         *string                `json:"instructions"`
	PreviousResponseID   *string                `json:"previous_response_id"`
	Error                json.RawMessage        `json:"error"`
	IncompleteDetails    json.RawMessage        `json:"incomplete_details"`
	SafetyIdentifier     *string                `json:"safety_identifier"`
	PromptCacheKey       *string                `json:"prompt_cache_key"`
	PromptCacheRetention *string                `json:"prompt_cache_retention"`
}

// WireCompactResource represents the OpenResponses response.compaction resource.
type WireCompactResource struct {
	ID        string     `json:"id"`
	Object    string     `json:"object"`
	CreatedAt int64      `json:"created_at"`
	Status    string     `json:"status"`
	Model     string     `json:"model"`
	Output    []WireItem `json:"output"`
	Usage     WireUsage  `json:"usage"`
}

// EnvelopeMetadata carries proxy-owned response metadata for resource construction.
type EnvelopeMetadata struct {
	ResponseID         string
	PreviousResponseID string
	CreatedAt          time.Time
	CompletedAt        *time.Time
	Model              string
	Status             string
	Store              *bool
}
