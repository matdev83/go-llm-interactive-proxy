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
