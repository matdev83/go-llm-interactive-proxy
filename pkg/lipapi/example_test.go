package lipapi_test

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func ExampleTextPart() {
	p := lipapi.TextPart("hello")
	fmt.Println(string(p.Kind), p.Text)
	// Output: text hello
}

func ExampleNewFixedEventStream() {
	ctx := context.Background()
	st := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "hi"},
		{Kind: lipapi.EventResponseFinished},
	})
	out, err := lipapi.Collect(ctx, st)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(out.Text.String())
	// Output: hi
}

func ExampleCall_Validate() {
	c := lipapi.Call{
		ID: "call-1",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("ping")},
		}},
	}
	if err := c.Validate(); err != nil {
		fmt.Println("invalid:", err)
		return
	}
	fmt.Println("valid")
	// Output: valid
}

func ExampleNewBackendCaps() {
	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools)
	_, hasStream := caps[lipapi.CapabilityStreaming]
	_, hasVision := caps[lipapi.CapabilityVision]
	fmt.Println(hasStream, hasVision)
	// Output: true false
}
