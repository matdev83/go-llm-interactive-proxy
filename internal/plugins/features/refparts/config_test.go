package refparts

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDecodeConfig_empty(t *testing.T) {
	t.Parallel()
	cfg, err := DecodeConfig(yaml.Node{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Order != nil || cfg.Suffix != "" || cfg.ResponsePrefix != "" {
		t.Fatalf("%+v", cfg)
	}
}

func TestDecodeConfig_null(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`null`), &n); err != nil {
		t.Fatal(err)
	}
	cfg, err := DecodeConfig(n)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Order != nil {
		t.Fatalf("%+v", cfg)
	}
}

func TestDecodeConfig_valid(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("order: 1\nsuffix: sfx\nresponse_prefix: pre"), &n); err != nil {
		t.Fatal(err)
	}
	cfg, err := DecodeConfig(n)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Order == nil || *cfg.Order != 1 || cfg.Suffix != "sfx" || cfg.ResponsePrefix != "pre" {
		t.Fatalf("%+v", cfg)
	}
}

func TestDecodeConfig_negativeOrder(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("order: -1"), &n); err != nil {
		t.Fatal(err)
	}
	_, err := DecodeConfig(n)
	if err == nil {
		t.Fatal("expected error for negative order")
	}
}

func TestDecodeConfig_invalidType(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`"some string"`), &n); err != nil {
		t.Fatal(err)
	}
	_, err := DecodeConfig(n)
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
}

func TestDecodeConfig_sequenceType(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`- 1`), &n); err != nil {
		t.Fatal(err)
	}
	_, err := DecodeConfig(n)
	if err == nil {
		t.Fatal("expected error for sequence type")
	}
}

func TestDecodeConfig_decodeError(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`order: "not an int"`), &n); err != nil {
		t.Fatal(err)
	}
	_, err := DecodeConfig(n)
	if err == nil {
		t.Fatal("expected error for decode failure")
	}
}
