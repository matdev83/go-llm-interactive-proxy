package lipruntime_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

// Task 2.3: every supported public reload name must be an exact alias of the
// canonical SDK contract — assignment identity with no conversion.
func TestReloadPublicAliases_AssignmentIdentityWithSDK(t *testing.T) {
	t.Parallel()

	var (
		_ sdkreload.TriggerKind     = lipruntime.TriggerKind("")
		_ lipruntime.TriggerKind    = sdkreload.TriggerKind("")
		_ sdkreload.ResultCategory  = lipruntime.ResultCategory("")
		_ lipruntime.ResultCategory = sdkreload.ResultCategory("")
		_ sdkreload.Trigger         = lipruntime.ReloadTrigger{}
		_ lipruntime.ReloadTrigger  = sdkreload.Trigger{}
		_ sdkreload.Result          = lipruntime.ReloadResult{}
		_ lipruntime.ReloadResult   = sdkreload.Result{}
		_ sdkreload.Status          = lipruntime.ReloadStatus{}
		_ lipruntime.ReloadStatus   = sdkreload.Status{}
		_ sdkreload.HistoryEntry    = lipruntime.HistoryEntry{}
		_ lipruntime.HistoryEntry   = sdkreload.HistoryEntry{}
	)

	cases := []struct {
		name string
		pub  any
		sdk  any
	}{
		{"TriggerKind", lipruntime.TriggerKind(""), sdkreload.TriggerKind("")},
		{"ResultCategory", lipruntime.ResultCategory(""), sdkreload.ResultCategory("")},
		{"ReloadTrigger", lipruntime.ReloadTrigger{}, sdkreload.Trigger{}},
		{"ReloadResult", lipruntime.ReloadResult{}, sdkreload.Result{}},
		{"ReloadStatus", lipruntime.ReloadStatus{}, sdkreload.Status{}},
		{"HistoryEntry", lipruntime.HistoryEntry{}, sdkreload.HistoryEntry{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pt, st := reflect.TypeOf(tc.pub), reflect.TypeOf(tc.sdk)
			if pt != st {
				t.Fatalf("%s public type %v is not identical to SDK type %v (want exact alias)", tc.name, pt, st)
			}
		})
	}
}

func TestReloadPublicAliases_ConstantReexportsMatchSDK(t *testing.T) {
	t.Parallel()
	consts := []struct {
		name string
		pub  any
		sdk  any
	}{
		{"TriggerSIGHUP", lipruntime.TriggerSIGHUP, sdkreload.TriggerSIGHUP},
		{"TriggerAPI", lipruntime.TriggerAPI, sdkreload.TriggerAPI},
		{"ResultPublished", lipruntime.ResultPublished, sdkreload.ResultPublished},
		{"ResultNoop", lipruntime.ResultNoop, sdkreload.ResultNoop},
		{"ResultBusy", lipruntime.ResultBusy, sdkreload.ResultBusy},
		{"ResultRestartRequired", lipruntime.ResultRestartRequired, sdkreload.ResultRestartRequired},
		{"ResultRetentionBlocked", lipruntime.ResultRetentionBlocked, sdkreload.ResultRetentionBlocked},
		{"ResultInvalid", lipruntime.ResultInvalid, sdkreload.ResultInvalid},
		{"ResultSourceIntegrity", lipruntime.ResultSourceIntegrity, sdkreload.ResultSourceIntegrity},
		{"ResultCanceled", lipruntime.ResultCanceled, sdkreload.ResultCanceled},
		{"ResultPreparationFailed", lipruntime.ResultPreparationFailed, sdkreload.ResultPreparationFailed},
		{"ResultInternalFailed", lipruntime.ResultInternalFailed, sdkreload.ResultInternalFailed},
	}
	for _, tc := range consts {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !reflect.DeepEqual(tc.pub, tc.sdk) {
				t.Fatalf("%s pub=%v sdk=%v", tc.name, tc.pub, tc.sdk)
			}
			if reflect.TypeOf(tc.pub) != reflect.TypeOf(tc.sdk) {
				t.Fatalf("%s type drift pub=%v sdk=%v", tc.name, reflect.TypeOf(tc.pub), reflect.TypeOf(tc.sdk))
			}
		})
	}
	if len(lipruntime.AllResultCategories) == 0 || len(lipruntime.AllResultCategories) != len(sdkreload.AllResultCategories) {
		t.Fatal("AllResultCategories must re-export the canonical vocabulary length")
	}
	if &lipruntime.AllResultCategories[0] != &sdkreload.AllResultCategories[0] {
		t.Fatal("AllResultCategories must share the canonical slice header")
	}
}

func TestReloadFacade_NilContextReturnsInternalFailed(t *testing.T) {
	t.Parallel()
	ctrl := lipruntime.NewReloadControlForTest(&fakeReloadQuery{
		reload: func(context.Context, sdkreload.Trigger) sdkreload.Result {
			t.Fatal("nil context must not reach coordinator")
			return sdkreload.Result{}
		},
	})
	got := ctrl.Reload(nil, lipruntime.ReloadTrigger{Kind: lipruntime.TriggerAPI}) //nolint:staticcheck // intentional nil ctx
	if got.Category != lipruntime.ResultInternalFailed || got.ReasonCategory != "nil-context" {
		t.Fatalf("got=%+v", got)
	}
}

func TestReloadFacade_UnavailableControlReturnsInternalFailed(t *testing.T) {
	t.Parallel()
	var ctrl *lipruntime.ReloadControl
	got := ctrl.Reload(context.Background(), lipruntime.ReloadTrigger{Kind: lipruntime.TriggerAPI})
	if got.Category != lipruntime.ResultInternalFailed || got.ReasonCategory != "reload-unavailable" {
		t.Fatalf("got=%+v", got)
	}
	if st := ctrl.Status(); st.Busy {
		t.Fatal("nil control must not report busy")
	}
}

func TestReloadFacade_NoConversionRoundTrip(t *testing.T) {
	t.Parallel()
	accepted := time.Unix(42, 0).UTC()
	var saw sdkreload.Trigger
	ctrl := lipruntime.NewReloadControlForTest(&fakeReloadQuery{
		reload: func(_ context.Context, trigger sdkreload.Trigger) sdkreload.Result {
			saw = trigger
			return sdkreload.Result{
				Category:         sdkreload.ResultPublished,
				AttemptID:        7,
				ActiveGeneration: 3,
				RestartFields:    []string{"server.address"},
			}
		},
	})
	in := lipruntime.ReloadTrigger{Kind: lipruntime.TriggerAPI, AcceptedAt: accepted, SafeActor: "actor"}
	got := ctrl.Reload(context.Background(), in)
	if saw.Kind != sdkreload.TriggerAPI || saw.AcceptedAt != accepted || saw.SafeActor != "actor" {
		t.Fatalf("coordinator saw %+v", saw)
	}
	// Result is the same underlying type — no mapper conversion required.
	var asSDK sdkreload.Result = got
	if asSDK.Category != sdkreload.ResultPublished || asSDK.AttemptID != 7 {
		t.Fatalf("result=%+v", asSDK)
	}
}
