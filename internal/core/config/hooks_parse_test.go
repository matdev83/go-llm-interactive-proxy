package config

import (
	"testing"

	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

func TestParseToolReactorErrorPolicy(t *testing.T) {
	t.Parallel()
	if ParseToolReactorErrorPolicy("") != sdk.ToolReactorErrorsFailOpen {
		t.Fatal("default")
	}
	if ParseToolReactorErrorPolicy("fail_closed") != sdk.ToolReactorErrorsFailClosed {
		t.Fatal("closed")
	}
	if ParseToolReactorErrorPolicy("Swallow") != sdk.ToolReactorErrorsSwallowEvent {
		t.Fatal("swallow")
	}
}
