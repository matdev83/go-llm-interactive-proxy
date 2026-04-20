package lipsdk_test

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestRegistrationAndConfigTypesAreConstructible(t *testing.T) {
	t.Parallel()

	var (
		reg lipsdk.Registration
		_   lipsdk.ConfigPayload
		_   lipsdk.PluginKind
		_   lipsdk.Requirement
	)
	reg = lipsdk.Registration{
		ID:     "x",
		Kind:   lipsdk.PluginKindFrontend,
		Config: lipsdk.ConfigPayload{Node: yaml.Node{Kind: yaml.MappingNode}},
	}
	_ = reg
}
