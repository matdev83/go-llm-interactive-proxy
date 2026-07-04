package controlplane_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestCategoryConstantsAreStable(t *testing.T) {
	t.Parallel()
	want := map[controlplane.Category]string{
		controlplane.CategoryAuth:      "auth",
		controlplane.CategorySession:   "session",
		controlplane.CategoryAttempt:   "attempt",
		controlplane.CategoryUsage:     "usage",
		controlplane.CategoryPolicy:    "policy",
		controlplane.CategoryAudit:     "audit",
		controlplane.CategoryLifecycle: "lifecycle",
	}
	for got, w := range want {
		if string(got) != w {
			t.Fatalf("category drift: got %q want %q", got, w)
		}
		if !got.IsKnown() {
			t.Fatalf("category %q must be known", got)
		}
	}
	if controlplane.Category("bogus").IsKnown() {
		t.Fatalf("unknown category reported as known")
	}
}

func TestEvidenceStateConstantsAreStable(t *testing.T) {
	t.Parallel()
	want := map[controlplane.EvidenceState]string{
		controlplane.EvidenceRecorded:    "recorded",
		controlplane.EvidencePartial:     "partial",
		controlplane.EvidenceUnavailable: "unavailable",
		controlplane.EvidenceRedacted:    "redacted",
		controlplane.EvidenceExpired:     "expired",
		controlplane.EvidenceUnsupported: "unsupported",
	}
	for got, w := range want {
		if string(got) != w {
			t.Fatalf("evidence state drift: got %q want %q", got, w)
		}
		if !got.IsKnown() {
			t.Fatalf("evidence state %q must be known", got)
		}
	}
}

func TestRedactionStateConstantsAreStable(t *testing.T) {
	t.Parallel()
	want := map[controlplane.RedactionState]string{
		controlplane.RedactionNone:       "none",
		controlplane.RedactionSummarized: "summarized",
		controlplane.RedactionRedacted:   "redacted",
		controlplane.RedactionHashed:     "hashed",
		controlplane.RedactionPrivileged: "privileged",
	}
	for got, w := range want {
		if string(got) != w {
			t.Fatalf("redaction state drift: got %q want %q", got, w)
		}
		if !got.IsKnown() {
			t.Fatalf("redaction state %q must be known", got)
		}
	}
}

func TestVisibilityConstantsAreStable(t *testing.T) {
	t.Parallel()
	if controlplane.VisibilityDefault != "default" {
		t.Fatalf("visibility default drift: %q", controlplane.VisibilityDefault)
	}
	if controlplane.VisibilityPrivileged != "privileged" {
		t.Fatalf("visibility privileged drift: %q", controlplane.VisibilityPrivileged)
	}
	if !controlplane.VisibilityDefault.IsKnown() || !controlplane.VisibilityPrivileged.IsKnown() {
		t.Fatalf("known visibility values must report known")
	}
	if controlplane.Visibility("bogus").IsKnown() {
		t.Fatalf("unknown visibility reported known")
	}
}

func TestEventIDZeroSemantics(t *testing.T) {
	t.Parallel()
	if !(controlplane.EventID{}).IsZero() {
		t.Fatalf("zero EventID must report zero")
	}
	if (controlplane.EventID{StoreID: "s", Sequence: 1}).IsZero() {
		t.Fatalf("non-zero EventID reported zero")
	}
}

func TestEventRequiresExactlyOneDetail(t *testing.T) {
	t.Parallel()
	now := time.Now()
	base := controlplane.Event{
		Category:       controlplane.CategoryAuth,
		OccurredAt:     now,
		RecordedAt:     now,
		Visibility:     controlplane.VisibilityDefault,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionNone,
	}
	if err := base.Validate(); err == nil {
		t.Fatalf("event with no detail must be invalid")
	}
	withAuth := base
	withAuth.Auth = &controlplane.AuthDetail{Outcome: "allow"}
	if err := withAuth.Validate(); err != nil {
		t.Fatalf("event with one auth detail must be valid: %v", err)
	}
	two := withAuth
	two.Session = &controlplane.SessionDetail{Action: controlplane.SessionActionCreated}
	if err := two.Validate(); err == nil {
		t.Fatalf("event with two details must be invalid")
	}
}

func TestEventRejectsCategoryDetailMismatch(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ev := controlplane.Event{
		Category:       controlplane.CategoryUsage,
		OccurredAt:     now,
		RecordedAt:     now,
		Visibility:     controlplane.VisibilityDefault,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionNone,
		Auth:           &controlplane.AuthDetail{Outcome: "allow"},
	}
	if err := ev.Validate(); err == nil {
		t.Fatalf("event with auth detail under usage category must be invalid")
	}
}

func TestEventJSONRoundTripPreservesDetailAndState(t *testing.T) {
	t.Parallel()
	now := time.Time{}.Add(time.Second)
	ev := controlplane.Event{
		ID:             controlplane.EventID{StoreID: "mem", Sequence: 7},
		SourceEventKey: "auth:trace-1:allow:ok",
		Category:       controlplane.CategoryAuth,
		OccurredAt:     now,
		RecordedAt:     now,
		Correlation:    controlplane.Correlation{TraceID: "trace-1", SessionID: "sess-1"},
		Source:         controlplane.SourceRef{Name: "authsink", Version: "v1"},
		Visibility:     controlplane.VisibilityDefault,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionNone,
		Auth:           &controlplane.AuthDetail{Outcome: "allow", ReasonCode: "ok"},
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back controlplane.Event
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Category != ev.Category {
		t.Fatalf("category lost: %q vs %q", back.Category, ev.Category)
	}
	if back.Auth == nil || back.Auth.Outcome != "allow" {
		t.Fatalf("auth detail lost: %#v", back.Auth)
	}
	if back.Session != nil || back.Attempt != nil {
		t.Fatalf("other details leaked through JSON: %#v", back)
	}
}

func TestEventHasNoForbiddenSecretFields(t *testing.T) {
	t.Parallel()
	forbidden := []string{"Bearer", "APIKey", "Secret", "OAuth", "Header", "Password", "RawPayload", "RawBody", "RawUsageJSON", "ResumeToken", "AccessToken", "RefreshToken", "IDToken"}
	var ev controlplane.Event
	assertNoForbiddenFields(t, ev, forbidden)
	var corr controlplane.Correlation
	assertNoForbiddenFields(t, corr, forbidden)
	var snap controlplane.ScopeSnapshot
	assertNoForbiddenFields(t, snap, forbidden)
}

func TestScopeSnapshotPreservesUnknownVsKnownEmpty(t *testing.T) {
	t.Parallel()
	snap := controlplane.ScopeSnapshot{
		Principal: scope.PrincipalScopeView{PrincipalID: scope.Known("u1")},
		TenantID:  scope.Unknown(),
		ProjectID: scope.Known(""),
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back controlplane.ScopeSnapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !back.TenantID.IsUnknown() {
		t.Fatalf("unknown tenant must round-trip as unknown")
	}
	if !back.ProjectID.IsKnownEmpty() {
		t.Fatalf("known-empty project must round-trip as known-empty")
	}
	if back.Principal.PrincipalID.String() != "u1" {
		t.Fatalf("principal lost: %q", back.Principal.PrincipalID.String())
	}
}

func TestRecordResultRoundTrip(t *testing.T) {
	t.Parallel()
	r := controlplane.RecordResult{
		ID:         controlplane.EventID{StoreID: "mem", Sequence: 3},
		Dedupe:     controlplane.DedupeInserted,
		RecordedAt: time.Now(),
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), "\"dedupe\":\"inserted\"") {
		t.Fatalf("dedupe constant drift: %s", raw)
	}
}

func assertNoForbiddenFields[T any](t *testing.T, v T, forbidden []string) {
	t.Helper()
	walkFieldNames(t, reflect.TypeOf(v), forbidden, map[reflect.Type]bool{})
}

func walkFieldNames(t *testing.T, rt reflect.Type, forbidden []string, seen map[reflect.Type]bool) {
	t.Helper()
	if rt == nil {
		return
	}
	if rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		return
	}
	if seen[rt] {
		return
	}
	seen[rt] = true
	for f := range rt.NumField() {
		fld := rt.Field(f)
		for _, bad := range forbidden {
			if strings.Contains(fld.Name, bad) {
				t.Fatalf("field %s.%s contains forbidden substring %q (raw secret/transport must not be in control-plane contract)", rt.Name(), fld.Name, bad)
			}
		}
		walkFieldNames(t, fld.Type, forbidden, seen)
	}
}
