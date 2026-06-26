package frontendconfig

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDecodeRejectsUnknownKeys(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("expose_lip_usage_extension: true"), &n); err != nil {
		t.Fatal(err)
	}
	_, err := Decode(n, "test")
	if err == nil || !strings.Contains(err.Error(), `unknown config key "expose_lip_usage_extension"`) {
		t.Fatalf("err = %v", err)
	}
}
