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

func ExampleValidateRegistrations() {
	registrations := []lipsdk.Registration{
		{ID: "ui", Kind: lipsdk.PluginKindFrontend, Enabled: true},
	}
	required := []lipsdk.Requirement{
		{Kind: lipsdk.PluginKindFrontend, ID: "ui"},
	}
	if err := lipsdk.ValidateRegistrations(registrations, required); err != nil {
		fmt.Println("invalid:", err)
		return
	}
	fmt.Println("ok")
	// Output: ok
}
