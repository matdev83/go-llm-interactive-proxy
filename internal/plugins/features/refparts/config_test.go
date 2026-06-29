package refparts

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDecodeConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		input          string
		wantError      string
		wantOrder      *int
		wantSuffix     string
		wantRespPrefix string
	}{
		{
			name:      "empty string",
			input:     "",
			wantError: "",
		},
		{
			name:      "null scalar",
			input:     "null",
			wantError: "",
		},
		{
			name:           "valid mapping",
			input:          "order: 5\nsuffix: foo\nresponse_prefix: bar",
			wantOrder:      intPtr(5),
			wantSuffix:     "foo",
			wantRespPrefix: "bar",
		},
		{
			name:      "negative order",
			input:     "order: -1",
			wantError: "order must be non-negative",
		},
		{
			name:      "invalid type (array)",
			input:     "[]",
			wantError: "config must be a mapping or null",
		},
		{
			name:      "invalid type (scalar non-null)",
			input:     "foo",
			wantError: "config must be a mapping or null",
		},
		{
			name:      "decode error type mismatch",
			input:     "order: [invalid]",
			wantError: "ref-request-suffix: yaml:",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var n yaml.Node
			if err := yaml.Unmarshal([]byte(tt.input), &n); err != nil {
				if tt.input != "" {
					t.Fatalf("setup error: %v", err)
				}
			}

			cfg, err := DecodeConfig(n)

			if tt.wantError != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got none", tt.wantError)
				}
				if !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("expected error containing %q, got %q", tt.wantError, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantOrder != nil {
				if cfg.Order == nil {
					t.Errorf("expected order %d, got nil", *tt.wantOrder)
				} else if *cfg.Order != *tt.wantOrder {
					t.Errorf("expected order %d, got %d", *tt.wantOrder, *cfg.Order)
				}
			} else if cfg.Order != nil {
				t.Errorf("expected nil order, got %d", *cfg.Order)
			}

			if cfg.Suffix != tt.wantSuffix {
				t.Errorf("expected suffix %q, got %q", tt.wantSuffix, cfg.Suffix)
			}

			if cfg.ResponsePrefix != tt.wantRespPrefix {
				t.Errorf("expected response prefix %q, got %q", tt.wantRespPrefix, cfg.ResponsePrefix)
			}
		})
	}
}

func intPtr(i int) *int {
	return &i
}
