package lipruntime_test

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
)

// fakeReloadQuery mirrors runtimehost.Coordinator Reload/Status for facade mapping tests.
type fakeReloadQuery struct {
	mu     sync.Mutex
	busy   bool
	status configreload.ReloadStatus
	reload func(ctx context.Context, trigger configreload.ReloadTrigger) configreload.ReloadResult
}

func (f *fakeReloadQuery) Reload(ctx context.Context, trigger configreload.ReloadTrigger) configreload.ReloadResult {
	f.mu.Lock()
	fn := f.reload
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, trigger)
	}
	return configreload.ReloadResult{Category: configreload.ResultNoop, ActiveGeneration: 1}
}

func (f *fakeReloadQuery) Status() configreload.ReloadStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.status
	st.Busy = f.busy
	return st
}

func TestReloadFacade_ImportablePublicSurface(t *testing.T) {
	t.Parallel()
	var (
		_ lipruntime.TriggerKind
		_ lipruntime.ResultCategory
		_ lipruntime.ReloadTrigger
		_ lipruntime.ReloadResult
		_ lipruntime.ReloadStatus
		_ lipruntime.HistoryEntry
		_ = lipruntime.TriggerAPI
		_ = lipruntime.TriggerSIGHUP
		_ = lipruntime.ResultPublished
		_ = lipruntime.ResultNoop
		_ = lipruntime.ResultBusy
		_ = lipruntime.ResultRestartRequired
		_ = lipruntime.ResultRetentionBlocked
		_ = lipruntime.ResultCanceled
	)
	ctrl := lipruntime.NewReloadControlForTest(&fakeReloadQuery{})
	if ctrl == nil {
		t.Fatal("expected ReloadControl")
	}
	_ = ctrl.Reload(context.Background(), lipruntime.ReloadTrigger{Kind: lipruntime.TriggerAPI})
	_ = ctrl.Status()
}

func TestReloadFacade_ExactCategoryMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   configreload.ResultCategory
		want lipruntime.ResultCategory
	}{
		{"published", configreload.ResultPublished, lipruntime.ResultPublished},
		{"noop", configreload.ResultNoop, lipruntime.ResultNoop},
		{"busy", configreload.ResultBusy, lipruntime.ResultBusy},
		{"restart_required", configreload.ResultRestartRequired, lipruntime.ResultRestartRequired},
		{"retention_blocked", configreload.ResultRetentionBlocked, lipruntime.ResultRetentionBlocked},
		{"invalid", configreload.ResultInvalid, lipruntime.ResultInvalid},
		{"source_integrity", configreload.ResultSourceIntegrity, lipruntime.ResultSourceIntegrity},
		{"canceled", configreload.ResultCanceled, lipruntime.ResultCanceled},
		{"preparation_failed", configreload.ResultPreparationFailed, lipruntime.ResultPreparationFailed},
		{"internal_failed", configreload.ResultInternalFailed, lipruntime.ResultInternalFailed},
	}
	if len(cases) != len(configreload.AllResultCategories) {
		t.Fatalf("cases=%d AllResultCategories=%d", len(cases), len(configreload.AllResultCategories))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := &fakeReloadQuery{
				reload: func(context.Context, configreload.ReloadTrigger) configreload.ReloadResult {
					return configreload.ReloadResult{
						Category:           tc.in,
						AttemptID:          9,
						ActiveGeneration:   3,
						PreviousGeneration: 2,
						RestartFields:      []string{"server.address"},
						RestartFieldCount:  1,
						ReasonCategory:     "classify",
						CoalescedSignals:   2,
					}
				},
			}
			ctrl := lipruntime.NewReloadControlForTest(q)
			got := ctrl.Reload(context.Background(), lipruntime.ReloadTrigger{Kind: lipruntime.TriggerAPI})
			if got.Category != tc.want {
				t.Fatalf("category=%q want %q", got.Category, tc.want)
			}
			if got.AttemptID != 9 || got.ActiveGeneration != 3 || got.PreviousGeneration != 2 {
				t.Fatalf("ids=%+v", got)
			}
			if got.RestartFieldCount != 1 || len(got.RestartFields) != 1 || got.RestartFields[0] != "server.address" {
				t.Fatalf("restart fields=%v count=%d", got.RestartFields, got.RestartFieldCount)
			}
			if got.ReasonCategory != "classify" || got.CoalescedSignals != 2 {
				t.Fatalf("meta=%+v", got)
			}
		})
	}
}

func TestReloadFacade_PreservesBusyNoopRestartRetentionCanceled(t *testing.T) {
	t.Parallel()
	outcomes := []configreload.ResultCategory{
		configreload.ResultBusy,
		configreload.ResultNoop,
		configreload.ResultRestartRequired,
		configreload.ResultRetentionBlocked,
		configreload.ResultCanceled,
	}
	for _, cat := range outcomes {
		cat := cat
		t.Run(string(cat), func(t *testing.T) {
			t.Parallel()
			q := &fakeReloadQuery{
				reload: func(context.Context, configreload.ReloadTrigger) configreload.ReloadResult {
					return configreload.ReloadResult{Category: cat, ActiveGeneration: 4}
				},
				status: configreload.ReloadStatus{
					ActiveGeneration: 4,
					Busy:             cat == configreload.ResultBusy,
					LastResult:       configreload.ReloadResult{Category: cat, ActiveGeneration: 4},
				},
			}
			if cat == configreload.ResultBusy {
				q.busy = true
			}
			ctrl := lipruntime.NewReloadControlForTest(q)
			res := ctrl.Reload(context.Background(), lipruntime.ReloadTrigger{Kind: lipruntime.TriggerAPI})
			if string(res.Category) != string(cat) {
				t.Fatalf("reload category=%q want %q", res.Category, cat)
			}
			st := ctrl.Status()
			if string(st.LastResult.Category) != string(cat) {
				t.Fatalf("status last=%q want %q", st.LastResult.Category, cat)
			}
			if cat == configreload.ResultBusy && !st.Busy {
				t.Fatal("expected Busy status")
			}
		})
	}
}

func TestReloadFacade_ImmutableCopiedDTOs(t *testing.T) {
	t.Parallel()
	fields := []string{"server.address", "management.listen"}
	hist := []configreload.HistoryEntry{{
		AttemptID:         1,
		Trigger:           configreload.TriggerAPI,
		Category:          configreload.ResultRestartRequired,
		ActiveGeneration:  2,
		RestartFieldCount: 2,
		ReasonCategory:    "classify",
		SafeActor:         "mgmt",
		RecordedAt:        time.Unix(100, 0).UTC(),
	}}
	q := &fakeReloadQuery{
		reload: func(context.Context, configreload.ReloadTrigger) configreload.ReloadResult {
			return configreload.ReloadResult{
				Category:          configreload.ResultRestartRequired,
				RestartFields:     fields,
				RestartFieldCount: 2,
				ActiveGeneration:  2,
			}
		},
		status: configreload.ReloadStatus{
			ActiveGeneration: 2,
			LastResult: configreload.ReloadResult{
				Category:          configreload.ResultRestartRequired,
				RestartFields:     fields,
				RestartFieldCount: 2,
			},
			History: hist,
		},
	}
	ctrl := lipruntime.NewReloadControlForTest(q)

	res := ctrl.Reload(context.Background(), lipruntime.ReloadTrigger{Kind: lipruntime.TriggerAPI})
	if len(res.RestartFields) != 2 || res.RestartFields[0] != "server.address" {
		t.Fatalf("restart fields=%v", res.RestartFields)
	}
	res.RestartFields[0] = "mutated-dto"
	res2 := ctrl.Reload(context.Background(), lipruntime.ReloadTrigger{Kind: lipruntime.TriggerAPI})
	if res2.RestartFields[0] != "server.address" {
		t.Fatalf("mutating prior Reload DTO leaked into next result: %v", res2.RestartFields)
	}

	st := ctrl.Status()
	if len(st.History) != 1 || st.LastResult.RestartFields[0] != "server.address" {
		t.Fatalf("status=%+v", st)
	}
	st.History[0].ReasonCategory = "mutated-history"
	st.LastResult.RestartFields[0] = "status-mutated"
	st2 := ctrl.Status()
	if st2.History[0].ReasonCategory != "classify" {
		t.Fatalf("Status History must be a defensive copy; got %q", st2.History[0].ReasonCategory)
	}
	if st2.LastResult.RestartFields[0] != "server.address" {
		t.Fatalf("Status RestartFields must be a defensive copy; got %v", st2.LastResult.RestartFields)
	}
}

func TestReloadFacade_UnknownCategoryMapsInternalFailed(t *testing.T) {
	t.Parallel()
	q := &fakeReloadQuery{
		reload: func(context.Context, configreload.ReloadTrigger) configreload.ReloadResult {
			return configreload.ReloadResult{Category: configreload.ResultCategory("not-a-real-category")}
		},
	}
	ctrl := lipruntime.NewReloadControlForTest(q)
	got := ctrl.Reload(context.Background(), lipruntime.ReloadTrigger{Kind: lipruntime.TriggerAPI})
	if got.Category != lipruntime.ResultInternalFailed {
		t.Fatalf("category=%q want internal-failed", got.Category)
	}
}

func TestReloadFacade_RejectsUnknownTrigger(t *testing.T) {
	t.Parallel()
	called := false
	q := &fakeReloadQuery{
		reload: func(context.Context, configreload.ReloadTrigger) configreload.ReloadResult {
			called = true
			return configreload.ReloadResult{Category: configreload.ResultPublished}
		},
	}
	ctrl := lipruntime.NewReloadControlForTest(q)
	got := ctrl.Reload(context.Background(), lipruntime.ReloadTrigger{Kind: lipruntime.TriggerKind("watch")})
	if called {
		t.Fatal("unknown trigger must not reach coordinator")
	}
	if got.Category != lipruntime.ResultInvalid || got.ReasonCategory != "trigger" {
		t.Fatalf("got=%+v", got)
	}
}

func TestReloadFacade_NoSensitiveFields(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		"Path", "YAML", "Yaml", "Secret", "Password", "Token", "DSN", "DSN",
		"Raw", "Closer", "Built", "Config", "Handler", "Fingerprint", "Digest",
		"FixedSource", "SourcePath", "FilePath", "URL", "Uri", "Credential",
	}
	for _, typ := range []reflect.Type{
		reflect.TypeOf(lipruntime.ReloadTrigger{}),
		reflect.TypeOf(lipruntime.ReloadResult{}),
		reflect.TypeOf(lipruntime.ReloadStatus{}),
		reflect.TypeOf(lipruntime.HistoryEntry{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			name := typ.Field(i).Name
			for _, bad := range forbidden {
				if strings.Contains(name, bad) {
					t.Fatalf("%s.%s looks sensitive/forbidden for public facade", typ.Name(), name)
				}
			}
		}
	}

	q := &fakeReloadQuery{
		status: configreload.ReloadStatus{
			ActiveGeneration: 1,
			FixedSourcePath:  "/var/secrets/config.yaml",
			LastResult: configreload.ReloadResult{
				Category:       configreload.ResultPublished,
				ReasonCategory: "ok",
			},
			SourceIntegrity: "ok",
		},
	}
	ctrl := lipruntime.NewReloadControlForTest(q)
	st := ctrl.Status()
	raw := strings.ToLower(st.SourceIntegrity + st.LastResult.ReasonCategory + st.ModelGeneration)
	for _, needle := range []string{"/var/", "secret", "password", "postgres://", ".yaml"} {
		if strings.Contains(raw, needle) {
			t.Fatalf("status leaked %q via %+v", needle, st)
		}
	}
	// Ensure path from coordinator is not projected onto any string field.
	v := reflect.ValueOf(st)
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() == reflect.String && strings.Contains(f.String(), "/var/secrets") {
			t.Fatalf("field %s leaked path %q", v.Type().Field(i).Name, f.String())
		}
	}
}

func TestReloadFacade_ConcurrentReloadStatusSafe(t *testing.T) {
	t.Parallel()
	var calls sync.WaitGroup
	q := &fakeReloadQuery{
		reload: func(context.Context, configreload.ReloadTrigger) configreload.ReloadResult {
			time.Sleep(time.Millisecond)
			return configreload.ReloadResult{Category: configreload.ResultNoop, ActiveGeneration: 1}
		},
		status: configreload.ReloadStatus{
			ActiveGeneration: 1,
			LastSuccess:      configreload.ReloadResult{Category: configreload.ResultPublished, ActiveGeneration: 1},
		},
	}
	ctrl := lipruntime.NewReloadControlForTest(q)
	const n = 32
	calls.Add(n * 2)
	errCh := make(chan error, n*2)
	for i := 0; i < n; i++ {
		go func() {
			defer calls.Done()
			res := ctrl.Reload(context.Background(), lipruntime.ReloadTrigger{Kind: lipruntime.TriggerAPI})
			if res.Category != lipruntime.ResultNoop {
				errCh <- errString("reload category " + string(res.Category))
			}
		}()
		go func() {
			defer calls.Done()
			st := ctrl.Status()
			if st.ActiveGeneration != 1 {
				errCh <- errString("status generation")
			}
			_ = st.LastSuccess.Category
		}()
	}
	calls.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRuntime_ReloadStatusUnavailableWithoutCoordinator(t *testing.T) {
	t.Parallel()
	rt := &lipruntime.Runtime{}
	res := rt.Reload(context.Background(), lipruntime.ReloadTrigger{Kind: lipruntime.TriggerAPI})
	if res.Category != lipruntime.ResultInternalFailed {
		t.Fatalf("category=%q want internal-failed", res.Category)
	}
	st := rt.ReloadStatus()
	if st.Busy {
		t.Fatal("empty runtime must not report busy")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
