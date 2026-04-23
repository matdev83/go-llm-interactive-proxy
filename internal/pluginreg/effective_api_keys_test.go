package pluginreg

import (
	"reflect"
	"testing"
)

func TestEffectiveAPIKeys_yamlOnly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		yamlKey  string
		yamlKeys []string
		defaults []string
		want     []string
	}{
		{
			name:    "api_key_only",
			yamlKey: " k1 ",
			want:    []string{"k1"},
		},
		{
			name:     "api_keys_only",
			yamlKeys: []string{" a ", "b"},
			want:     []string{"a", "b"},
		},
		{
			name:     "api_key_then_api_keys",
			yamlKey:  "first",
			yamlKeys: []string{"second", "third"},
			want:     []string{"first", "second", "third"},
		},
		{
			name:     "dedupe_across_key_and_list",
			yamlKey:  "same",
			yamlKeys: []string{"same", "other"},
			want:     []string{"same", "other"},
		},
		{
			name:     "dedupe_in_list",
			yamlKeys: []string{"x", "x", "y"},
			want:     []string{"x", "y"},
		},
		{
			name:     "trim_and_drop_empty_list_entries",
			yamlKey:  "",
			yamlKeys: []string{"", "  ok  ", "\t"},
			want:     []string{"ok"},
		},
		{
			name:     "yaml_ignores_defaults_when_any_yaml_secret",
			yamlKey:  "yaml",
			yamlKeys: nil,
			defaults: []string{"env1", "env2"},
			want:     []string{"yaml"},
		},
		{
			name:     "yaml_list_non_empty_ignores_defaults",
			yamlKey:  "",
			yamlKeys: []string{"only"},
			defaults: []string{"env"},
			want:     []string{"only"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := EffectiveAPIKeys(tc.yamlKey, tc.yamlKeys, tc.defaults)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("EffectiveAPIKeys(...) = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestEffectiveAPIKeys_defaultsOnly(t *testing.T) {
	t.Parallel()
	got := EffectiveAPIKeys("", nil, []string{" d1 ", "", "d2", "d1"})
	want := []string{"d1", "d2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestEffectiveAPIKeys_allEmpty(t *testing.T) {
	t.Parallel()
	got := EffectiveAPIKeys("", []string{"", " "}, []string{})
	if len(got) != 0 {
		t.Fatalf("want empty slice, got %#v", got)
	}
}
