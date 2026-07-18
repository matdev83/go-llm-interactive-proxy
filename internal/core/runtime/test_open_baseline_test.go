package runtime

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

func testMinimalUserMessages() []lipapi.Message {
	return []lipapi.Message{{
		Role:  lipapi.RoleUser,
		Parts: []lipapi.Part{lipapi.TextPart("hi")},
	}}
}
