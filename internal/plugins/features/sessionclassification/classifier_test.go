package sessionclassification_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/sessionclassification"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	sdkclassification "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// fakeAuthority resolves one process-owned store per classifier and counts how
// often the classifier asked for it.
type fakeAuthority struct {
	mu    sync.Mutex
	store *fakeStore
	err   error
	calls int
}

func (a *fakeAuthority) ClassificationState() (sessionclassification.Store, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.err != nil {
		return nil, a.err
	}
	return a.store, nil
}

func (a *fakeAuthority) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// fakeStore is a minimal monotonic store: it keeps the first positive record
// per authority key and counts every operation without retaining raw evidence.
type fakeStore struct {
	mu       sync.Mutex
	records  map[sessionclassification.Key]sessionclassification.Record
	loads    int
	promote  int
	promoted []sessionclassification.Record
	loadErr  error
}

var _ sessionclassification.Store = (*fakeStore)(nil)

func newFakeStore() *fakeStore {
	return &fakeStore{records: make(map[sessionclassification.Key]sessionclassification.Record)}
}

func (s *fakeStore) Load(_ context.Context, key sessionclassification.Key) (sessionclassification.Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	if s.loadErr != nil {
		return sessionclassification.Record{}, false, s.loadErr
	}
	record, ok := s.records[key]
	return record, ok, nil
}

func (s *fakeStore) Promote(_ context.Context, key sessionclassification.Key, proposal session.Classification, now time.Time) (sessionclassification.Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[key]; ok && existing.Classification.IsCodingAgent() {
		return existing, false, nil
	}
	proposal.Revision = 1
	record := sessionclassification.Record{Key: key, Classification: proposal, UpdatedAt: now}
	s.records[key] = record
	s.promote++
	s.promoted = append(s.promoted, record)
	return record, true, nil
}

func (s *fakeStore) ClaimRemote(context.Context, sessionclassification.Key, time.Time, uint32, time.Duration, time.Duration) (sessionclassification.RemoteClaim, sessionclassification.Record, bool, error) {
	return sessionclassification.RemoteClaim{}, sessionclassification.Record{}, false, errors.New("remote claims are not part of the local classifier")
}

func (s *fakeStore) CompleteRemote(context.Context, sessionclassification.RemoteClaim, sessionclassification.RemoteCompletion, time.Time) (sessionclassification.Record, error) {
	return sessionclassification.Record{}, errors.New("remote completion is not part of the local classifier")
}

func (s *fakeStore) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads, s.promote
}

func (s *fakeStore) stored(key sessionclassification.Key) (sessionclassification.Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key]
	return record, ok
}

func mustClassifier(t *testing.T, cfg sessionclassification.Config, store *fakeStore, now func() time.Time) *sessionclassification.Classifier {
	t.Helper()
	classifier, err := sessionclassification.NewClassifier(cfg, sessionclassification.ClassifierDeps{
		State: &fakeAuthority{store: store},
		Now:   now,
	})
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}
	return classifier
}

func fixedNow() func() time.Time {
	instant := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	return func() time.Time { return instant }
}

// validTestRemoteConfig mirrors the documented operational values for the
// remote modes. The credential stays a referenced environment name.
func validTestRemoteConfig() *sessionclassification.RemoteConfig {
	return &sessionclassification.RemoteConfig{
		Provider:              "jev",
		APIKeyEnv:             "TYPESAFE_API_KEY",
		Timeout:               750 * time.Millisecond,
		MaxAttemptsPerSession: 1,
		LeaseTTL:              2 * time.Second,
		RetryBackoff:          0,
		PositiveThreshold:     0.90,
	}
}

func codingInput(sessionID, userAgent string) sdkclassification.Input {
	return sdkclassification.Input{
		Session:  session.SessionView{AuthoritativeSessionID: sessionID},
		Evidence: sdkclassification.Evidence{ClientUserAgent: userAgent},
	}
}

func TestNewClassifierRejectsUnusableConfiguration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cfg  sessionclassification.Config
		deps sessionclassification.ClassifierDeps
	}{
		{
			name: "missing state authority",
			cfg:  sessionclassification.Config{Mode: sessionclassification.ModeHeuristic},
			deps: sessionclassification.ClassifierDeps{},
		},
		{
			name: "unknown mode",
			cfg:  sessionclassification.Config{Mode: sessionclassification.Mode("telepathy")},
			deps: sessionclassification.ClassifierDeps{State: &fakeAuthority{store: newFakeStore()}},
		},
		{
			name: "remote settings without remote mode",
			cfg: sessionclassification.Config{
				Mode:   sessionclassification.ModeHeuristic,
				Remote: &sessionclassification.RemoteConfig{Provider: "jev"},
			},
			deps: sessionclassification.ClassifierDeps{State: &fakeAuthority{store: newFakeStore()}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			classifier, err := sessionclassification.NewClassifier(tc.cfg, tc.deps)
			if err == nil {
				t.Fatalf("NewClassifier(%+v) = %+v, want error", tc.cfg, classifier)
			}
			if classifier != nil {
				t.Fatalf("NewClassifier returned a classifier together with error %v", err)
			}
		})
	}
}

func TestNewClassifierNormalizesOmittedModeToHeuristic(t *testing.T) {
	t.Parallel()

	classifier, err := sessionclassification.NewClassifier(sessionclassification.Config{}, sessionclassification.ClassifierDeps{
		State: &fakeAuthority{store: newFakeStore()},
	})
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}
	got, err := classifier.Classify(t.Context(), codingInput("sess-mode", "codex_cli_rs/1.2.3"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !got.IsCodingAgent() {
		t.Fatalf("omitted-mode classification = %+v, want heuristic promotion", got)
	}
}

func TestClassifierIdentityIsTheStableFeatureIdentity(t *testing.T) {
	t.Parallel()

	classifier := mustClassifier(t, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, newFakeStore(), fixedNow())
	if classifier.ID() != sessionclassification.ID {
		t.Fatalf("classifier ID = %q, want %q", classifier.ID(), sessionclassification.ID)
	}
	var plane sdkclassification.Classifier = classifier
	if _, err := sdkclassification.ClassifierIdentity(plane); err != nil {
		t.Fatalf("ClassifierIdentity: %v", err)
	}
}

func TestClassifierHoldsNoNetworkCapableDependency(t *testing.T) {
	t.Parallel()

	// Requirements 6.1/6.2: composing or running this generation's classifier
	// cannot reach an external service. The concrete classifier holds only
	// bounded policy, a lazy state authority, and a clock, so no remote adapter
	// can be constructed or called until a future task deliberately binds one.
	typ := reflect.TypeOf(sessionclassification.Classifier{})
	allowed := map[string]reflect.Type{
		"cfg":   reflect.TypeOf(sessionclassification.Config{}),
		"state": reflect.TypeOf((*sessionclassification.StateAuthority)(nil)).Elem(),
		"now":   reflect.TypeOf((func() time.Time)(nil)),
	}
	if typ.NumField() != len(allowed) {
		t.Fatalf("classifier has %d fields, want exactly %d bounded fields", typ.NumField(), len(allowed))
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		want, ok := allowed[field.Name]
		if !ok {
			t.Errorf("classifier has unapproved field %q of type %s", field.Name, field.Type)
			continue
		}
		if field.Type != want {
			t.Errorf("classifier.%s has type %s, want %s", field.Name, field.Type, want)
		}
	}
}

func TestClassifyPromotesDecisiveIdentityOnceAndReplaysIdempotently(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	classifier := mustClassifier(t, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, store, fixedNow())
	in := codingInput("sess-codex", "codex_cli_rs/1.2.3")

	first, err := classifier.Classify(t.Context(), in)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	want := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh,
		Evidence:   "client_family.codex",
		Revision:   1,
	}
	if first != want {
		t.Fatalf("first classification = %+v, want %+v", first, want)
	}

	second, err := classifier.Classify(t.Context(), in)
	if err != nil {
		t.Fatalf("replayed Classify: %v", err)
	}
	if second != first {
		t.Fatalf("replayed classification = %+v, want identical %+v", second, first)
	}
	if _, promotes := store.counts(); promotes != 1 {
		t.Fatalf("durable promotions = %d, want exactly one first-accepted promotion", promotes)
	}
	key := sessionclassification.Key{Kind: sessionclassification.ScopeSecureSession, ID: "sess-codex"}
	record, ok := store.stored(key)
	if !ok {
		t.Fatal("promotion did not persist under the proxy-authority key")
	}
	if record.Classification != want {
		t.Fatalf("persisted classification = %+v, want %+v", record.Classification, want)
	}
	if strings.Contains(string(record.Classification.Evidence), "codex_cli_rs") {
		t.Fatalf("persisted evidence retained the raw User-Agent: %q", record.Classification.Evidence)
	}
}

func TestClassifyKeepsPriorPositiveWithoutTouchingProcessState(t *testing.T) {
	t.Parallel()

	authority := &fakeAuthority{store: newFakeStore()}
	classifier, err := sessionclassification.NewClassifier(sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, sessionclassification.ClassifierDeps{State: authority, Now: fixedNow()})
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}
	prior := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalTooling,
		Confidence: session.ConfidenceHigh,
		Evidence:   "tooling.distinct_coding_cluster",
		Revision:   7,
	}
	in := codingInput("sess-prior", "codex_cli_rs/1.2.3")
	in.Session.Classification = prior

	got, err := classifier.Classify(t.Context(), in)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != prior {
		t.Fatalf("classification = %+v, want the preserved positive %+v", got, prior)
	}
	if calls := authority.callCount(); calls != 0 {
		t.Fatalf("warm positive resolved process state %d times, want zero", calls)
	}
}

func TestClassifyWithoutProxyAuthorityKeepsCurrentSnapshot(t *testing.T) {
	t.Parallel()

	authority := &fakeAuthority{store: newFakeStore()}
	classifier, err := sessionclassification.NewClassifier(sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, sessionclassification.ClassifierDeps{State: authority, Now: fixedNow()})
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}
	in := codingInput("", "codex_cli_rs/1.2.3")
	in.Session = session.SessionView{ClientSessionHint: "client-controlled-hint"}

	got, err := classifier.Classify(t.Context(), in)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != (session.Classification{}) {
		t.Fatalf("classification = %+v, want unknown without proxy authority", got)
	}
	if calls := authority.callCount(); calls != 0 {
		t.Fatalf("classifier consulted process state %d times without authority, want zero", calls)
	}
}

func TestClassifyUsesALegAuthorityFallback(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	classifier := mustClassifier(t, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, store, fixedNow())
	in := sdkclassification.Input{
		Session:  session.SessionView{ALegID: "a-leg-42", ClientSessionHint: "shared-hint"},
		Evidence: sdkclassification.Evidence{ClientUserAgent: "roo-code/1.0.0"},
	}

	got, err := classifier.Classify(t.Context(), in)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Source != session.SourceLocalIdentity || got.Evidence != "client_family.roo" {
		t.Fatalf("classification = %+v, want A-leg scoped roo identity promotion", got)
	}
	if _, ok := store.stored(sessionclassification.Key{Kind: sessionclassification.ScopeALeg, ID: "a-leg-42"}); !ok {
		t.Fatal("A-leg promotion did not persist under the proxy-owned A-leg key")
	}
	if _, ok := store.stored(sessionclassification.Key{Kind: sessionclassification.ScopeALeg, ID: "shared-hint"}); ok {
		t.Fatal("client-controlled session hint was used as a state authority")
	}
}

func TestClassifyModeControlsWhichLocalEvidenceMayPromote(t *testing.T) {
	t.Parallel()

	distinctCluster := sdkclassification.ToolCategoryFileRead |
		sdkclassification.ToolCategoryFileEdit |
		sdkclassification.ToolCategoryOSCommand

	cases := []struct {
		name        string
		mode        sessionclassification.Mode
		wantPromote bool
		wantSource  session.ClassificationSource
		wantCode    session.EvidenceCode
	}{
		{name: "heuristic promotes decisive local identity", mode: sessionclassification.ModeHeuristic, wantPromote: true, wantSource: session.SourceLocalIdentity, wantCode: "client_family.codex"},
		{name: "jev never promotes from local evidence alone", mode: sessionclassification.ModeJev, wantPromote: false},
		{name: "hybrid promotes decisive local identity", mode: sessionclassification.ModeHybrid, wantPromote: true, wantSource: session.SourceLocalIdentity, wantCode: "client_family.codex"},
		{name: "heuristic promotes the distinct coding cluster", mode: sessionclassification.ModeHeuristic, wantPromote: true, wantSource: session.SourceLocalTooling, wantCode: "tooling.distinct_coding_cluster"},
		{name: "hybrid promotes the distinct coding cluster", mode: sessionclassification.ModeHybrid, wantPromote: true, wantSource: session.SourceLocalTooling, wantCode: "tooling.distinct_coding_cluster"},
		{name: "jev keeps the distinct coding cluster unknown", mode: sessionclassification.ModeJev, wantPromote: false},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			cfg := sessionclassification.Config{Mode: tc.mode}
			if tc.mode == sessionclassification.ModeJev || tc.mode == sessionclassification.ModeHybrid {
				cfg.Remote = validTestRemoteConfig()
			}
			classifier := mustClassifier(t, cfg, store, fixedNow())
			userAgent := ""
			if tc.wantSource == session.SourceLocalIdentity {
				userAgent = "codex_cli_rs/1.2.3"
			}
			in := sdkclassification.Input{
				Session:  session.SessionView{ALegID: "a-leg-mode-" + string(rune('a'+i))},
				Evidence: sdkclassification.Evidence{ClientUserAgent: userAgent, ToolCategories: distinctCluster},
			}

			got, err := classifier.Classify(t.Context(), in)
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if !tc.wantPromote {
				if got != (session.Classification{}) {
					t.Fatalf("classification = %+v, want unknown", got)
				}
				if _, promotes := store.counts(); promotes != 0 {
					t.Fatalf("durable promotions = %d, want zero", promotes)
				}
				return
			}
			if !got.IsCodingAgent() || got.Source != tc.wantSource || got.Evidence != tc.wantCode || got.Revision != 1 {
				t.Fatalf("classification = %+v, want %s/%s at revision 1", got, tc.wantSource, tc.wantCode)
			}
		})
	}
}

func TestClassifyReturnsPersistedPositiveForLaterWeakTurns(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	classifier := mustClassifier(t, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, store, fixedNow())
	strong := codingInput("sess-persist", "codex_cli_rs/1.2.3")
	if _, err := classifier.Classify(t.Context(), strong); err != nil {
		t.Fatalf("Classify: %v", err)
	}

	// A later turn that shares the client hint but carries no decisive evidence
	// still projects the authoritative positive value.
	weak := sdkclassification.Input{
		Session:  session.SessionView{AuthoritativeSessionID: "sess-persist", ClientSessionHint: "unrelated-hint"},
		Evidence: sdkclassification.Evidence{ClientUserAgent: "Mozilla/5.0"},
	}
	got, err := classifier.Classify(t.Context(), weak)
	if err != nil {
		t.Fatalf("later Classify: %v", err)
	}
	if !got.IsCodingAgent() || got.Evidence != "client_family.codex" {
		t.Fatalf("later classification = %+v, want the stored positive snapshot", got)
	}
}

func TestClassifyFailsOpenWhenProcessStateIsUnavailable(t *testing.T) {
	t.Parallel()

	loadFailure := errors.New("durable load failed")
	stateFailure := errors.New("state holder closed")
	cases := []struct {
		name      string
		authority *fakeAuthority
		storeErr  error
		want      session.Classification
	}{
		{name: "uninitialized state authority", authority: &fakeAuthority{err: stateFailure}},
		{name: "durable load failure", authority: &fakeAuthority{store: newFakeStore()}, storeErr: loadFailure},
	}
	for _, tc := range cases {
		if tc.storeErr != nil {
			tc.authority.store.loadErr = tc.storeErr
		}
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			classifier, err := sessionclassification.NewClassifier(sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, sessionclassification.ClassifierDeps{State: tc.authority, Now: fixedNow()})
			if err != nil {
				t.Fatalf("NewClassifier: %v", err)
			}
			got, err := classifier.Classify(t.Context(), codingInput("sess-failopen", "codex_cli_rs/1.2.3"))
			if err == nil {
				t.Fatalf("Classify error = nil, want a bounded state failure")
			}
			if got != tc.want {
				t.Fatalf("classification = %+v, want unknown after fail-open", got)
			}
		})
	}
}

func TestClassifyFailsOpenPreservingPriorPositiveOnStateFailure(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.loadErr = errors.New("durable load failed")
	classifier := mustClassifier(t, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, store, fixedNow())
	prior := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceRemote,
		Confidence: session.ConfidenceHigh,
		Evidence:   "client_family.opencode",
		Revision:   1,
	}
	in := codingInput("sess-remote", "codex_cli_rs/1.2.3")
	in.Session.Classification = prior

	got, err := classifier.Classify(t.Context(), in)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != prior {
		t.Fatalf("classification = %+v, want the preserved prior positive %+v", got, prior)
	}
}

func TestClassifyRequiresContext(t *testing.T) {
	t.Parallel()

	//nolint:staticcheck // A nil context must fail closed at the feature boundary.
	classifier := mustClassifier(t, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, newFakeStore(), fixedNow())
	got, err := classifier.Classify(nil, codingInput("sess-nilctx", "codex_cli_rs/1.2.3")) //nolint:staticcheck
	if err == nil {
		t.Fatal("Classify with a nil context returned no error")
	}
	if got != (session.Classification{}) {
		t.Fatalf("classification = %+v, want unknown", got)
	}
}

func TestClassifyNeverConstructsDurableNegativeState(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	classifier := mustClassifier(t, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, store, fixedNow())
	in := sdkclassification.Input{
		Session:   session.SessionView{ALegID: "a-leg-chat"},
		Evidence:  sdkclassification.Evidence{ClientUserAgent: "openai-python/1.40.0", ToolCategories: sdkclassification.ToolCategoryWebAccess},
		Workspace: workspace.WorkspaceView{Markers: []string{"go.mod"}},
	}

	got, err := classifier.Classify(t.Context(), in)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != (session.Classification{}) {
		t.Fatalf("technical-chat classification = %+v, want unknown", got)
	}
	if _, ok := store.stored(sessionclassification.Key{Kind: sessionclassification.ScopeALeg, ID: "a-leg-chat"}); ok {
		t.Fatal("unknown evidence created a durable negative classification row")
	}
	if loads, promotes := store.counts(); promotes != 0 {
		t.Fatalf("durable promotions = %d, want zero", promotes)
	} else if loads != 1 {
		t.Fatalf("durable loads = %d, want exactly one authoritative load per unknown turn", loads)
	}
}

func TestClassifyHonorsGenerationScopedExclusions(t *testing.T) {
	t.Parallel()

	excluded := mustClassifier(t, sessionclassification.Config{
		Mode:      sessionclassification.ModeHeuristic,
		Heuristic: sessionclassification.HeuristicConfig{IgnoredUserAgentPrefixes: []string{"codex_cli_rs"}},
	}, newFakeStore(), fixedNow())
	got, err := excluded.Classify(t.Context(), codingInput("sess-excluded", "codex_cli_rs/1.2.3"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != (session.Classification{}) {
		t.Fatalf("excluded classification = %+v, want unknown", got)
	}
}
