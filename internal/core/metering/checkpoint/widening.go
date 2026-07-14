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
	if c.Options.MaxOutputTokens != nil {
		b.WriteString("|MO=")
		b.WriteString(strconv.Itoa(*c.Options.MaxOutputTokens))
	}
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
			if len(p.Content) > 0 {
				b.WriteByte(':')
				b.Write(p.Content)
			}
		}
	}
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
