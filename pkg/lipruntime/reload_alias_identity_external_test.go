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

	acceptSDKTriggerKind := func(sdkreload.TriggerKind) {}
	acceptPublicTriggerKind := func(lipruntime.TriggerKind) {}
	acceptSDKResultCategory := func(sdkreload.ResultCategory) {}
	acceptPublicResultCategory := func(lipruntime.ResultCategory) {}
	acceptSDKTrigger := func(sdkreload.Trigger) {}
	acceptPublicTrigger := func(lipruntime.ReloadTrigger) {}
	acceptSDKResult := func(sdkreload.Result) {}
	acceptPublicResult := func(lipruntime.ReloadResult) {}
	acceptSDKStatus := func(sdkreload.Status) {}
	acceptPublicStatus := func(lipruntime.ReloadStatus) {}
	acceptSDKHistory := func(sdkreload.HistoryEntry) {}
	acceptPublicHistory := func(lipruntime.HistoryEntry) {}

	acceptSDKTriggerKind(lipruntime.TriggerKind(""))
	acceptPublicTriggerKind(sdkreload.TriggerKind(""))
	acceptSDKResultCategory(lipruntime.ResultCategory(""))
	acceptPublicResultCategory(sdkreload.ResultCategory(""))
	acceptSDKTrigger(lipruntime.ReloadTrigger{})
	acceptPublicTrigger(sdkreload.Trigger{})
	acceptSDKResult(lipruntime.ReloadResult{})
	acceptPublicResult(sdkreload.Result{})
	acceptSDKStatus(lipruntime.ReloadStatus{})
	acceptPublicStatus(sdkreload.Status{})
	acceptSDKHistory(lipruntime.HistoryEntry{})
	acceptPublicHistory(sdkreload.HistoryEntry{})

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
	pub := lipruntime.ResultCategories()
	sdk := sdkreload.ResultCategories()
	if len(pub) == 0 || len(pub) != len(sdk) {
		t.Fatal("ResultCategories must re-export the canonical vocabulary length")
	}
	if !reflect.DeepEqual(pub, sdk) {
		t.Fatalf("ResultCategories pub=%v sdk=%v", pub, sdk)
	}
	// The alias must preserve defensive-copy semantics: mutating the public result
	// must not leak into the SDK accessor.
	pub[0] = "decoy"
	if sdkreload.ResultCategories()[0] == "decoy" {
		t.Fatal("ResultCategories must return an independent copy per call")
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
	asSDK := func(v sdkreload.Result) sdkreload.Result { return v }(got)
	if asSDK.Category != sdkreload.ResultPublished || asSDK.AttemptID != 7 {
		t.Fatalf("result=%+v", asSDK)
	}
}
