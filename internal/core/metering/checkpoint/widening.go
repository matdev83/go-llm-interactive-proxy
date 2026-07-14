package checkpoint

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// BillableFingerprint is a stable encoding of billable call content used to
// detect unmeasured widening after an authorized backend-ingress freeze.
func BillableFingerprint(c lipapi.Call) ([]byte, error) {
	type fp struct {
		Instructions []lipapi.Message      `json:"instructions"`
		Messages     []lipapi.Message      `json:"messages"`
		Tools        []lipapi.ToolDef      `json:"tools"`
		ToolChoice   lipapi.ToolChoice     `json:"tool_choice"`
		MaxOut       *int                  `json:"max_output_tokens"`
		Temp         *float64              `json:"temperature"`
		TopP         *float64              `json:"top_p"`
	}
	payload := fp{
		Instructions: c.Instructions,
		Messages:     c.Messages,
		Tools:        c.Tools,
		ToolChoice:   c.ToolChoice,
		MaxOut:       c.Options.MaxOutputTokens,
		Temp:         c.Options.Temperature,
		TopP:         c.Options.TopP,
	}
	return json.Marshal(payload)
}

// BillableWidened reports whether current has billable content beyond authorized.
func BillableWidened(authorized, current lipapi.Call) (bool, error) {
	a, err := BillableFingerprint(authorized)
	if err != nil {
		return false, err
	}
	b, err := BillableFingerprint(current)
	if err != nil {
		return false, err
	}
	return !bytes.Equal(a, b), nil
}

// ErrUnmeasuredWidening is returned when a call changes billable content after
// the authorized backend-ingress freeze (requirement 7.5).
var ErrUnmeasuredWidening = fmt.Errorf("metering/checkpoint: unmeasured post-authorization widening")

// AssertNotWidened returns ErrUnmeasuredWidening when current differs from authorized.
func AssertNotWidened(authorized, current lipapi.Call) error {
	widened, err := BillableWidened(authorized, current)
	if err != nil {
		return err
	}
	if widened {
		return ErrUnmeasuredWidening
	}
	return nil
}
