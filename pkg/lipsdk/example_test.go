package lipsdk_test

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func ExampleRequirement() {
	r := lipsdk.Requirement{Kind: lipsdk.PluginKindFrontend, ID: "openairesponses"}
	fmt.Println(string(r.Kind), r.ID)
	// Output: frontend openairesponses
}
