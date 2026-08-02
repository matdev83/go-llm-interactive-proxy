package standardplugins

import (
	"errors"
	"strings"
)

var ErrCustomBackendPrefix = errors.New("custom backend prefix")

const (
	CustomOpenAILegacyCompatibleID    = "custom-openai-legacy-compatible"
	CustomOpenAIResponsesCompatibleID = "custom-openai-responses-compatible"
	CustomAnthropicCompatibleID       = "custom-anthropic-compatible"
)

func IsCustomCompatibleBackendKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case CustomOpenAILegacyCompatibleID, CustomOpenAIResponsesCompatibleID, CustomAnthropicCompatibleID, CustomOpenResponsesCompatibleID:
		return true
	default:
		return false
	}
}
