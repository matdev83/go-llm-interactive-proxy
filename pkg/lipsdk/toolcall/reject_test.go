package toolcall_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
)

func TestRejectError_noRawPayload(t *testing.T) {
	t.Parallel()
	err := &toolcall.RejectError{ReasonCode: toolcall.ReasonUnrepairable, ToolCallID: "c1"}
	msg := err.Error()
	if !strings.Contains(msg, toolcall.ReasonUnrepairable) {
		t.Fatalf("msg=%q", msg)
	}
	if strings.Contains(msg, "{") || strings.Contains(msg, "args") {
		t.Fatalf("must not include payload: %q", msg)
	}
	var re *toolcall.RejectError
	if !errors.As(err, &re) {
		t.Fatal("errors.As")
	}
}
