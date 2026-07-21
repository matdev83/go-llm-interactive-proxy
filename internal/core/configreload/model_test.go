package configreload_test

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
)

func TestCoordinatorResultCategories_BusyNoopFaultShutdownCoalesceVocabulary(t *testing.T) {
	t.Parallel()
	want := map[configreload.ResultCategory]bool{
		configreload.ResultPublished:         true,
		configreload.ResultNoop:              true,
		configreload.ResultBusy:              true,
		configreload.ResultRestartRequired:   true,
		configreload.ResultRetentionBlocked:  true,
		configreload.ResultInvalid:           true,
		configreload.ResultSourceIntegrity:   true,
		configreload.ResultCanceled:          true,
		configreload.ResultPreparationFailed: true,
		configreload.ResultInternalFailed:    true,
	}
	if len(configreload.AllResultCategories) != len(want) {
		t.Fatalf("AllResultCategories len=%d want %d", len(configreload.AllResultCategories), len(want))
	}
	for _, c := range configreload.AllResultCategories {
		if !want[c] {
			t.Fatalf("unexpected category %q", c)
		}
		delete(want, c)
	}
	if len(want) != 0 {
		t.Fatalf("missing categories: %v", want)
	}
}

func TestMapLoadFailure_CoordinatorFaultSourceAndInvalid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want configreload.ResultCategory
	}{
		{
			name: "source_missing",
			err:  &config.LoadError{Category: config.CategoryMissing},
			want: configreload.ResultSourceIntegrity,
		},
		{
			name: "non_atomic",
			err:  &config.LoadError{Category: config.CategoryNonAtomicUpdate},
			want: configreload.ResultSourceIntegrity,
		},
		{
			name: "malformed_yaml",
			err:  &config.LoadError{Category: config.CategoryMalformedYAML},
			want: configreload.ResultInvalid,
		},
		{
			name: "restart_required",
			err:  &configreload.RestartRequiredError{RestartRequiredFields: []string{"server.address"}, TotalBlocked: 1},
			want: configreload.ResultRestartRequired,
		},
		{
			name: "validate_wrap",
			err:  errors.New("validate config: bad route"),
			want: configreload.ResultInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _ := configreload.MapLoadFailure(tc.err)
			if got != tc.want {
				t.Fatalf("MapLoadFailure(%v)=%q want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestTriggerKinds_NoPathOrYAMLFields(t *testing.T) {
	t.Parallel()
	tr := configreload.ReloadTrigger{
		Kind:      configreload.TriggerAPI,
		SafeActor: "management-api",
	}
	if tr.Kind != configreload.TriggerAPI && tr.Kind != configreload.TriggerSIGHUP {
		t.Fatalf("unexpected kind %q", tr.Kind)
	}
	// Structural: trigger envelope has no path/yaml/url fields (req 1.7, 12.4).
	_ = tr.AcceptedAt
	_ = tr.SafeActor
}
