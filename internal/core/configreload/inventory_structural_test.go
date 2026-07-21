package configreload_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
)

// TestUnclassifiedTopLevelField_StructuralGuardFails uses reflection only in
// tests to ensure every YAML-tagged top-level Config field is inventoried.
func TestUnclassifiedTopLevelField_StructuralGuardFails(t *testing.T) {
	t.Parallel()
	yamlFields := topLevelYAMLFields(t, reflect.TypeOf(config.Config{}))
	inv := map[string]bool{}
	for _, e := range configreload.Inventory() {
		if strings.HasPrefix(e.Path, "override.") {
			continue
		}
		// Top-level inventory paths are bare section names.
		top := e.Path
		if i := strings.IndexByte(e.Path, '.'); i >= 0 {
			top = e.Path[:i]
		}
		inv[top] = true
	}
	var missing []string
	for _, f := range yamlFields {
		if !inv[f] {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("unclassified top-level config fields (add inventory entries): %v", missing)
	}
}

func TestUnclassifiedStartupOverride_StructuralGuardFails(t *testing.T) {
	t.Parallel()
	inv := map[string]bool{}
	for _, e := range configreload.Inventory() {
		inv[e.Path] = true
	}
	var missing []string
	for _, path := range configreload.RequiredStartupOverridePaths() {
		if !inv[path] {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("unclassified startup overrides: %v", missing)
	}
}

func TestUnclassifiedFieldDetection_HelperRejectsGap(t *testing.T) {
	t.Parallel()
	// Proves the guard would fail when a newly added field lacks classification.
	declared := []string{"server", "access", "brand_new_section"}
	inventoried := []string{"server", "access"}
	missing := configreload.MissingClassifications(declared, inventoried)
	if len(missing) != 1 || missing[0] != "brand_new_section" {
		t.Fatalf("MissingClassifications = %v, want [brand_new_section]", missing)
	}
}

func topLevelYAMLFields(t *testing.T, typ reflect.Type) []string {
	t.Helper()
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	return out
}
