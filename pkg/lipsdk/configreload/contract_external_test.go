package configreload_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

// TestExternalPackage_ConstructsCanonicalContractWithoutInternalImport proves that
// an external consumer can build every canonical reload contract value by importing
// only pkg/lipsdk/configreload (req 7.1-7.3).
func TestExternalPackage_ConstructsCanonicalContractWithoutInternalImport(t *testing.T) {
	t.Parallel()

	kind := configreload.TriggerAPI
	_ = configreload.TriggerSIGHUP
	tr := configreload.Trigger{
		Kind:       kind,
		AcceptedAt: time.Unix(1, 0).UTC(),
		SafeActor:  "management-api",
	}
	res := configreload.Result{
		Category:           configreload.ResultPublished,
		AttemptID:          1,
		ActiveGeneration:   2,
		PreviousGeneration: 1,
		RestartFields:      []string{"server.address"},
		RestartFieldCount:  1,
		ReasonCategory:     "classify",
		CoalescedSignals:   0,
	}
	hist := configreload.HistoryEntry{
		AttemptID:           1,
		Trigger:             tr.Kind,
		Stage:               "publish",
		Category:            res.Category,
		ActiveGeneration:    2,
		CandidateGeneration: 3,
		DurationMs:          10,
		RestartFieldCount:   1,
		ReasonCategory:      "publish",
		SafeActor:           tr.SafeActor,
		RecordedAt:          tr.AcceptedAt,
	}
	st := configreload.Status{
		ActiveGeneration:    2,
		CurrentAttempt:      &res,
		LastResult:          res,
		LastSuccess:         res,
		LastFailure:         configreload.Result{Category: configreload.ResultNoop},
		SourceIntegrity:     "ok",
		RetainedGenerations: 1,
		RetentionPressure:   false,
		ControlDegraded:     false,
		ModelGeneration:     "m1",
		History:             []configreload.HistoryEntry{hist},
		Busy:                true,
		PendingSignal:       false,
		CoalescedSignals:    0,
	}
	if tr.Kind != configreload.TriggerAPI {
		t.Fatalf("trigger kind=%q", tr.Kind)
	}
	if st.LastResult.Category != configreload.ResultPublished {
		t.Fatalf("status category=%q", st.LastResult.Category)
	}
	if len(configreload.AllResultCategories) == 0 {
		t.Fatal("AllResultCategories must be non-empty")
	}
	// Compile-time inventory of every canonical named type.
	var (
		_ configreload.TriggerKind
		_ configreload.ResultCategory
		_ configreload.Trigger
		_ configreload.Result
		_ configreload.Status
		_ configreload.HistoryEntry
	)
}

func TestClosedVocabulary_ExactTriggerAndResultCategories(t *testing.T) {
	t.Parallel()

	wantTriggers := map[configreload.TriggerKind]bool{
		configreload.TriggerSIGHUP: true,
		configreload.TriggerAPI:    true,
	}
	for k := range wantTriggers {
		if k == "" {
			t.Fatal("empty trigger kind must not be a declared constant")
		}
	}
	if configreload.TriggerKind("") == configreload.TriggerAPI {
		t.Fatal("zero-value TriggerKind must not equal TriggerAPI")
	}
	if configreload.TriggerKind("unknown") == configreload.TriggerAPI ||
		configreload.TriggerKind("unknown") == configreload.TriggerSIGHUP {
		t.Fatal("unknown trigger kind must not equal closed constants")
	}

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

	// Unknown/zero-value policy: empty stays empty; unknown maps to internal-failed at boundaries.
	if got := configreload.NormalizeResultCategory(""); got != "" {
		t.Fatalf("empty category normalize=%q want empty", got)
	}
	if got := configreload.NormalizeResultCategory("brand-new"); got != configreload.ResultInternalFailed {
		t.Fatalf("unknown category normalize=%q want %q", got, configreload.ResultInternalFailed)
	}
	if got := configreload.NormalizeResultCategory(configreload.ResultBusy); got != configreload.ResultBusy {
		t.Fatalf("known category normalize=%q", got)
	}
	if !configreload.IsKnownTriggerKind(configreload.TriggerAPI) || configreload.IsKnownTriggerKind("") ||
		configreload.IsKnownTriggerKind("path") {
		t.Fatal("IsKnownTriggerKind closed-set policy mismatch")
	}
}

// TestNormalizeResultCategory_IndependentOfMutableAllResultCategories proves
// normalization/known-category policy does not consult the exported mutable
// AllResultCategories slice header. Must not run in parallel: mutates package
// state and restores it.
func TestNormalizeResultCategory_IndependentOfMutableAllResultCategories(t *testing.T) {
	orig := append([]configreload.ResultCategory(nil), configreload.AllResultCategories...)
	t.Cleanup(func() {
		configreload.AllResultCategories = append([]configreload.ResultCategory(nil), orig...)
	})

	declared := []configreload.ResultCategory{
		configreload.ResultPublished,
		configreload.ResultNoop,
		configreload.ResultBusy,
		configreload.ResultRestartRequired,
		configreload.ResultRetentionBlocked,
		configreload.ResultInvalid,
		configreload.ResultSourceIntegrity,
		configreload.ResultCanceled,
		configreload.ResultPreparationFailed,
		configreload.ResultInternalFailed,
	}

	// Corrupt the exported slice: clear, reorder, and inject decoys.
	configreload.AllResultCategories = []configreload.ResultCategory{
		"decoy-a",
		configreload.ResultBusy,
		"decoy-b",
	}

	for _, c := range declared {
		if got := configreload.NormalizeResultCategory(c); got != c {
			t.Fatalf("after AllResultCategories mutation, NormalizeResultCategory(%q)=%q want self", c, got)
		}
	}
	if got := configreload.NormalizeResultCategory(""); got != "" {
		t.Fatalf("empty normalize=%q want empty", got)
	}
	if got := configreload.NormalizeResultCategory("brand-new"); got != configreload.ResultInternalFailed {
		t.Fatalf("unknown normalize=%q want %q", got, configreload.ResultInternalFailed)
	}
	if got := configreload.NormalizeResultCategory("decoy-a"); got != configreload.ResultInternalFailed {
		t.Fatalf("decoy injected into AllResultCategories must not become known: got %q", got)
	}
}

func TestSecretSafeShape_RejectsForbiddenFields(t *testing.T) {
	t.Parallel()

	forbidden := map[string]bool{
		"FixedSourcePath": true,
		"Path":            true,
		"SourcePath":      true,
		"ConfigPath":      true,
		"YAML":            true,
		"Yaml":            true,
		"RawYAML":         true,
		"Config":          true,
		"Effective":       true,
		"Credential":      true,
		"Credentials":     true,
		"Password":        true,
		"Token":           true,
		"DSN":             true,
		"Digest":          true,
		"PrivateDigest":   true,
		"SourceDigest":    true,
		"APIKey":          true,
		"Secret":          true,
	}

	for _, rt := range []reflect.Type{
		reflect.TypeFor[configreload.Trigger](),
		reflect.TypeFor[configreload.Result](),
		reflect.TypeFor[configreload.Status](),
		reflect.TypeFor[configreload.HistoryEntry](),
	} {
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if forbidden[f.Name] {
				t.Fatalf("%s must not expose forbidden field %q", rt.Name(), f.Name)
			}
			switch f.Type.Kind() {
			case reflect.Map, reflect.Chan, reflect.Func:
				t.Fatalf("%s.%s must not use kind %s", rt.Name(), f.Name, f.Type.Kind())
			}
			if f.Type.Kind() == reflect.Interface && f.Type.NumMethod() == 0 {
				t.Fatalf("%s.%s must not be any/interface{}", rt.Name(), f.Name)
			}
		}
	}

	st := reflect.TypeFor[configreload.Status]()
	if _, ok := st.FieldByName("FixedSourcePath"); ok {
		t.Fatal("canonical Status must not include FixedSourcePath")
	}
}

func TestDefensiveCopy_StatusResultHistoryBoundaries(t *testing.T) {
	t.Parallel()

	fields := []string{"server.address", "tls.cert"}
	res := configreload.Result{
		Category:      configreload.ResultRestartRequired,
		RestartFields: fields,
	}
	clonedRes := res.Clone()
	fields[0] = "mutated"
	if clonedRes.RestartFields[0] != "server.address" {
		t.Fatal("Result.Clone must copy RestartFields")
	}
	clonedRes.RestartFields[1] = "other"
	if res.RestartFields[1] != "tls.cert" {
		t.Fatal("Result.Clone must not alias RestartFields")
	}

	cur := configreload.Result{Category: configreload.ResultBusy, RestartFields: []string{"a"}}
	hist := []configreload.HistoryEntry{{AttemptID: 1, SafeActor: "api"}}
	st := configreload.Status{
		CurrentAttempt: &cur,
		LastResult:     cur,
		History:        hist,
	}
	cloned := st.Clone()
	cur.RestartFields[0] = "mutated-cur"
	hist[0].SafeActor = "mutated-actor"
	st.CurrentAttempt.Category = configreload.ResultPublished
	if cloned.CurrentAttempt == nil || cloned.CurrentAttempt.RestartFields[0] != "a" {
		t.Fatal("Status.Clone must deep-copy CurrentAttempt")
	}
	if cloned.CurrentAttempt.Category != configreload.ResultBusy {
		t.Fatal("Status.Clone must snapshot CurrentAttempt value")
	}
	if cloned.History[0].SafeActor != "api" {
		t.Fatal("Status.Clone must copy History entries")
	}
	cloned.History[0].SafeActor = "x"
	if st.History[0].SafeActor == "x" {
		t.Fatal("Status.Clone must not alias History slice")
	}
	if cloned.History == nil && st.History != nil {
		t.Fatal("nil/empty history policy mismatch")
	}
}
