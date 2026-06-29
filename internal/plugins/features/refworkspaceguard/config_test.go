package refworkspaceguard_test

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refworkspaceguard"
)

func TestDecodeConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		check   func(*testing.T, refworkspaceguard.Config)
	}{
		{
			name:    "empty config",
			yaml:    "",
			wantErr: false,
			check: func(t *testing.T, c refworkspaceguard.Config) {
				t.Helper()
				if c.ProjectRoot != "/ref/workspace" {
					t.Errorf("expected default project root, got %s", c.ProjectRoot)
				}
			},
		},
		{
			name:    "null config",
			yaml:    "null",
			wantErr: false,
			check: func(t *testing.T, c refworkspaceguard.Config) {
				t.Helper()
				if c.ProjectRoot != "/ref/workspace" {
					t.Errorf("expected default project root, got %s", c.ProjectRoot)
				}
			},
		},
		{
			name:    "valid mapping",
			yaml:    "project_root: /custom/path\ndirty_tree: false\nmarkers:\n  - .refws",
			wantErr: false,
			check: func(t *testing.T, c refworkspaceguard.Config) {
				t.Helper()
				if c.ProjectRoot != "/custom/path" {
					t.Errorf("expected /custom/path, got %s", c.ProjectRoot)
				}
				if c.DirtyTree != false {
					t.Errorf("expected DirtyTree false, got %v", c.DirtyTree)
				}
				if len(c.Markers) != 1 || c.Markers[0] != ".refws" {
					t.Errorf("expected markers [.refws], got %v", c.Markers)
				}
			},
		},
		{
			name:    "valid mapping empty project root (gets defaults with order)",
			yaml:    "order: 5\ndirty_tree: false",
			wantErr: false,
			check: func(t *testing.T, c refworkspaceguard.Config) {
				t.Helper()
				if c.ProjectRoot != "/ref/workspace" {
					t.Errorf("expected default project root, got %s", c.ProjectRoot)
				}
				if c.Order == nil || *c.Order != 5 {
					t.Errorf("expected order 5, got %v", c.Order)
				}
			},
		},
		{
			name:    "negative order",
			yaml:    "order: -1",
			wantErr: true,
		},
		{
			name:    "invalid type",
			yaml:    "- some_list_item",
			wantErr: true,
		},
		{
			name:    "scalar non-null",
			yaml:    "some_string",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var n yaml.Node
			err := yaml.Unmarshal([]byte(tt.yaml), &n)
			if err != nil {
				t.Fatalf("failed to unmarshal yaml: %v", err)
			}

			cfg, err := refworkspaceguard.DecodeConfig(n)
			if (err != nil) != tt.wantErr {
				t.Fatalf("DecodeConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}
