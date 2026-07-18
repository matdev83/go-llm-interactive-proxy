package checkpoint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// BillableFingerprint is a stable encoding of billable call content used to
// detect unmeasured widening after an authorized backend-ingress freeze.
// It avoids json.Marshal of json.RawMessage fields (tool Parameters) so invalid
// or non-JSON parameter bytes do not fail Open.
func BillableFingerprint(c lipapi.Call) ([]byte, error) {
	var b strings.Builder
	writeMessages(&b, "I", c.Instructions)
	writeMessages(&b, "M", c.Messages)
	for i, tool := range c.Tools {
		b.WriteString("|T")
		b.WriteString(strconv.Itoa(i))
		b.WriteByte('=')
		b.WriteString(tool.Name)
		b.WriteByte(':')
		b.WriteString(string(tool.Parameters))
	}
	b.WriteString("|TC=")
	b.WriteString(string(c.ToolChoice.Mode))
	// MaxOutputTokens is compared separately so authority clamps may narrow the
	// bound after freeze without failing the no-widening invariant (req 7.5).
	if c.Options.Temperature != nil {
		b.WriteString("|TP=")
		b.WriteString(strconv.FormatFloat(*c.Options.Temperature, 'g', -1, 64))
	}
	if c.Options.TopP != nil {
		b.WriteString("|P=")
		b.WriteString(strconv.FormatFloat(*c.Options.TopP, 'g', -1, 64))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return []byte(hex.EncodeToString(sum[:])), nil
}

func maxOutputTokensOrNeg(c lipapi.Call) int {
	if c.Options.MaxOutputTokens == nil {
		return -1
	}
	return *c.Options.MaxOutputTokens
}

func writeMessages(b *strings.Builder, tag string, msgs []lipapi.Message) {
	for i, m := range msgs {
		b.WriteString("|")
		b.WriteString(tag)
		b.WriteString(strconv.Itoa(i))
		b.WriteByte('=')
		b.WriteString(string(m.Role))
		for j, p := range m.Parts {
			b.WriteByte('#')
			b.WriteString(strconv.Itoa(j))
			b.WriteByte(':')
			b.WriteString(string(p.Kind))
			b.WriteByte(':')
			b.WriteString(p.Text)
			if p.Kind == lipapi.PartReasoning && p.Reasoning != nil {
				b.WriteString(":R")
				writeLenFrame(b, string(p.Reasoning.Dialect))
				writeLenFrame(b, p.Reasoning.Text)
				writeLenFrame(b, p.Reasoning.Signature)
				writeLenFrame(b, string(p.Reasoning.Opaque))
			}
			if len(p.Content) > 0 {
				b.WriteByte(':')
				b.Write(p.Content)
			}
		}
	}
}

func writeLenFrame(b *strings.Builder, s string) {
	b.WriteString(strconv.Itoa(len(s)))
	b.WriteByte(':')
	b.WriteString(s)
}

// BillableWidened reports whether current has billable content beyond authorized.
// Lowering MaxOutputTokens (authority/preflight clamp) is narrowing, not widening.
// Raising MaxOutputTokens or introducing a max when the freeze had none is widening.
func BillableWidened(authorized, current lipapi.Call) (bool, error) {
	a, err := BillableFingerprint(authorized)
	if err != nil {
		return false, err
	}
	b, err := BillableFingerprint(current)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(a, b) {
		return true, nil
	}
	authMO := maxOutputTokensOrNeg(authorized)
	curMO := maxOutputTokensOrNeg(current)
	switch {
	case authMO < 0 && curMO < 0:
		return false, nil
	case authMO < 0 && curMO >= 0:
		// Freeze had unbounded output; binding a max is not billable content widening.
		return false, nil
	case authMO >= 0 && curMO < 0:
		return true, nil
	case curMO > authMO:
		return true, nil
	default:
		return false, nil
	}
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
