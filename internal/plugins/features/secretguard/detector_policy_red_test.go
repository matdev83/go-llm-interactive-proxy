package secretguard

import (
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// TestDecodeConfig_DetectorControlsPreservePresence is a RED characterization
// for the presence-aware detector configuration described by the BetterLeaks
// secret-guard design. Ordinary bool zero values cannot distinguish an omitted
// switch from an explicit false; composition needs that distinction to resolve
// access-mode-dependent defaults.
func TestDecodeConfig_DetectorControlsPreservePresence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		yaml        string
		wantPresent bool
		wantValue   bool
	}{
		{name: "absent", yaml: "action: block\n", wantPresent: false},
		{name: "true", yaml: "action: block\nauto_discovered_local_keys:\n  enabled: true\n", wantPresent: true, wantValue: true},
		{name: "false", yaml: "action: block\nauto_discovered_local_keys:\n  enabled: false\n", wantPresent: true, wantValue: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := DecodeConfig(mustYAML(t, tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			assertPresenceAwareEnabled(t, cfg, "AutoDiscoveredLocalKeys", tc.wantPresent, tc.wantValue)

			cfg, err = DecodeConfig(mustYAML(t, strings.Replace(tc.yaml, "auto_discovered_local_keys", "betterleaks", 1)))
			if err != nil {
				t.Fatal(err)
			}
			assertPresenceAwareEnabled(t, cfg, "BetterLeaks", tc.wantPresent, tc.wantValue)
		})
	}
}

// TestComposeRuntimeConfig_ResolvesDetectorDefaultsCharacterization records
// the effective detector posture required at generation composition. The
// fields are intentionally checked by name here because task 1.1 is the RED
// contract for the feature-private runtime model; task 1.2 supplies the model.
func TestComposeRuntimeConfig_ResolvesDetectorDefaultsCharacterization(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		accessMode    string
		local         string
		betterLeaks   string
		wantLocal     bool
		wantBetter    bool
		wantErrorPart string
	}{
		{name: "single_absent_absent", accessMode: "single_user", local: "", betterLeaks: "", wantLocal: true, wantBetter: true},
		{name: "single_false_true", accessMode: "single_user", local: "false", betterLeaks: "true", wantLocal: false, wantBetter: true},
		{name: "single_true_false", accessMode: "single_user", local: "true", betterLeaks: "false", wantLocal: true, wantBetter: false},
		{name: "multi_absent_absent", accessMode: "multi_user", local: "", betterLeaks: "", wantLocal: false, wantBetter: true},
		{name: "multi_false_false", accessMode: "multi_user", local: "false", betterLeaks: "false", wantLocal: false, wantBetter: false},
		{name: "multi_true_absent_rejected", accessMode: "multi_user", local: "true", betterLeaks: "", wantErrorPart: "auto_discovered_local_keys"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := "action: block\n"
			if tc.local != "" {
				raw += "auto_discovered_local_keys:\n  enabled: " + tc.local + "\n"
			}
			if tc.betterLeaks != "" {
				raw += "betterleaks:\n  enabled: " + tc.betterLeaks + "\n"
			}
			regs := []lipsdk.Registration{{
				Kind: lipsdk.PluginKindFeature, ID: "detector-policy", FactoryKind: ID, Enabled: true,
				Config: lipsdk.ConfigPayload{Node: mustYAML(t, raw)},
			}}
			got, err := ComposeRuntimeConfig(tc.accessMode, regs)
			if tc.wantErrorPart != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrorPart) {
					t.Fatalf("expected bounded configuration error containing %q, got %v", tc.wantErrorPart, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			assertRuntimeBool(t, got, "LocalAutoDiscoveryEnabled", tc.wantLocal)
			assertRuntimeBool(t, got, "BetterLeaksEnabled", tc.wantBetter)
		})
	}
}

func assertPresenceAwareEnabled(t *testing.T, cfg Config, fieldName string, wantPresent, wantValue bool) {
	t.Helper()
	field := reflect.ValueOf(cfg).FieldByName(fieldName)
	if !field.IsValid() {
		t.Fatalf("Config must expose presence-aware %s detector settings", fieldName)
	}
	if field.Kind() != reflect.Struct {
		t.Fatalf("Config.%s must retain a structured detector subtree, got %s", fieldName, field.Kind())
	}
	enabled := field.FieldByName("Enabled")
	if !enabled.IsValid() {
		t.Fatalf("Config.%s must expose enabled state", fieldName)
	}

	switch enabled.Kind() {
	case reflect.Pointer:
		if enabled.Type().Elem().Kind() != reflect.Bool {
			t.Fatalf("Config.%s.Enabled pointer must point to bool, got %s", fieldName, enabled.Type().Elem())
		}
		if enabled.IsNil() != !wantPresent {
			t.Fatalf("Config.%s.Enabled presence=%t, want %t", fieldName, !enabled.IsNil(), wantPresent)
		}
		if wantPresent && enabled.Elem().Bool() != wantValue {
			t.Fatalf("Config.%s.Enabled=%t, want %t", fieldName, enabled.Elem().Bool(), wantValue)
		}
	case reflect.Bool:
		present := false
		for _, name := range []string{"Present", "EnabledPresent", "EnabledSet", "HasEnabled"} {
			marker := field.FieldByName(name)
			if marker.IsValid() && marker.Kind() == reflect.Bool {
				present = marker.Bool()
				break
			}
		}
		if present != wantPresent || (wantPresent && enabled.Bool() != wantValue) {
			t.Fatalf("Config.%s presence/value=(%t,%t), want (%t,%t)", fieldName, present, enabled.Bool(), wantPresent, wantValue)
		}
	default:
		t.Fatalf("Config.%s.Enabled must be *bool or bool, got %s", fieldName, enabled.Kind())
	}
}

func assertRuntimeBool(t *testing.T, cfg RuntimeConfig, fieldName string, want bool) {
	t.Helper()
	field := reflect.ValueOf(cfg).FieldByName(fieldName)
	if !field.IsValid() || field.Kind() != reflect.Bool {
		t.Fatalf("RuntimeConfig must expose boolean %s, got %v", fieldName, field)
	}
	if field.Bool() != want {
		t.Fatalf("RuntimeConfig.%s=%t, want %t", fieldName, field.Bool(), want)
	}
}
