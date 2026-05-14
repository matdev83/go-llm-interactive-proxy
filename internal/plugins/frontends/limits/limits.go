package limits

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Adapter-local decode limits. These intentionally mirror canonical validation
// caps but fail before protocol adapters allocate, copy, or reparse large values.
const (
	MaxMessages       = lipapi.MaxMessages
	MaxParts          = lipapi.MaxPartsPerMessage
	MaxTools          = lipapi.MaxTools
	MaxToolSchema     = lipapi.MaxToolParametersBytes
	MaxRawJSONPayload = lipapi.MaxPartJSONBytes
	MaxMetadata       = 256
	MaxBase64Data     = lipapi.MaxRefStringBytes
)

func Count(field string, got, max int) error {
	if got > max {
		return fmt.Errorf("%s has %d entries; maximum is %d", field, got, max)
	}
	return nil
}

func Bytes(field string, got, max int) error {
	if got > max {
		return fmt.Errorf("%s has %d bytes; maximum is %d", field, got, max)
	}
	return nil
}

func StringBytes(field, value string, max int) error {
	return Bytes(field, len(value), max)
}
