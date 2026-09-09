package largebody_test

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// canonicalOnlyExecutor is an external/manual executor: it implements only the
// public lipsdk.ExecutorView contract and knows nothing about the internal
// large-body capability.
type canonicalOnlyExecutor struct {
	executeCalls int
}

func (e *canonicalOnlyExecutor) Execute(context.Context, *lipapi.Call) (lipapi.EventStream, error) {
	e.executeCalls++
	return stubStream{}, nil
}

func (e *canonicalOnlyExecutor) CancelALeg(context.Context, lipapi.ALegCancelRequest) error {
	return nil
}

func (e *canonicalOnlyExecutor) WallClock() func() time.Time { return nil }

// capableExecutor is a standard internal executor: public view plus the
// internal optional large-body capability.
type capableExecutor struct {
	canonicalOnlyExecutor
	assessCalls  int
	executeCalls int
}

func (e *capableExecutor) AssessLargeBody(_ context.Context, req largebody.AssessmentRequest) (largebody.AssessmentResult, error) {
	e.assessCalls++
	return largebody.AssessmentResult{
		Decision: largebody.AssessmentDecisionDecline,
		Reason:   largebody.DeclineReasonAuthorityBlocker,
	}, nil
}

func (e *capableExecutor) ExecuteLargeBody(_ context.Context, _ largebody.AssessmentStamp, _ largebody.Source) (largebody.ExecutionResult, error) {
	e.executeCalls++
	return largebody.ExecutionResult{}, nil
}

var (
	_ lipsdk.ExecutorView = (*canonicalOnlyExecutor)(nil)
	_ lipsdk.ExecutorView = (*capableExecutor)(nil)
)

func executorViewMethodNames(t *testing.T) []string {
	t.Helper()
	typ := reflect.TypeOf((*lipsdk.ExecutorView)(nil)).Elem()
	if typ.Kind() != reflect.Interface {
		t.Fatalf("lipsdk.ExecutorView is %s, want interface", typ.Kind())
	}
	var names []string
	for i := 0; i < typ.NumMethod(); i++ {
		names = append(names, typ.Method(i).Name)
	}
	sort.Strings(names)
	return names
}

func TestExecutorView_PublicContractUnchanged(t *testing.T) {
	names := executorViewMethodNames(t)
	want := []string{"CancelALeg", "Execute", "WallClock"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("lipsdk.ExecutorView methods = %v, want %v (no mandatory large-body methods)", names, want)
	}
	for _, forbidden := range []string{"AssessLargeBody", "ExecuteLargeBody", "LargeBodyStaticDisposition"} {
		for _, name := range names {
			if name == forbidden {
				t.Fatalf("lipsdk.ExecutorView must not gain mandatory method %q", forbidden)
			}
		}
	}
}

func TestLargeBodyCapability_DefinedInternally(t *testing.T) {
	typ := reflect.TypeOf((*largebody.LargeBodyExecutor)(nil)).Elem()
	if typ.Kind() != reflect.Interface {
		t.Fatalf("largebody.LargeBodyExecutor is %s, want interface", typ.Kind())
	}
	var names []string
	for i := 0; i < typ.NumMethod(); i++ {
		names = append(names, typ.Method(i).Name)
	}
	sort.Strings(names)
	want := []string{"AssessLargeBody", "ExecuteLargeBody"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("largebody.LargeBodyExecutor methods = %v, want %v", names, want)
	}
	if typ.PkgPath() == "" || typ.PkgPath() == "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk" {
		t.Fatalf("large-body capability lives in %q, must stay internal (not public SDK)", typ.PkgPath())
	}
}

func TestAsLargeBodyExecutor_NilAndAbsentYieldCanonical(t *testing.T) {
	if got, ok := largebody.AsLargeBodyExecutor(nil); ok || got != nil {
		t.Fatalf("AsLargeBodyExecutor(nil) = (%v, %v), want (nil, false)", got, ok)
	}
	var nilView lipsdk.ExecutorView
	if got, ok := largebody.AsLargeBodyExecutor(nilView); ok || got != nil {
		t.Fatalf("AsLargeBodyExecutor(typed-nil view) = (%v, %v), want (nil, false)", got, ok)
	}
	canonical := &canonicalOnlyExecutor{}
	if got, ok := largebody.AsLargeBodyExecutor(canonical); ok || got != nil {
		t.Fatalf("AsLargeBodyExecutor(canonical-only) = (%v, %v), want (nil, false)", got, ok)
	}
	capable := &capableExecutor{}
	got, ok := largebody.AsLargeBodyExecutor(capable)
	if !ok || got == nil {
		t.Fatalf("AsLargeBodyExecutor(capable) = (%v, %v), want (non-nil, true)", got, ok)
	}
	if _, err := got.AssessLargeBody(context.Background(), largebody.AssessmentRequest{}); err == nil && capable.assessCalls != 1 {
		t.Fatalf("assessCalls = %d, want 1 via asserted capability", capable.assessCalls)
	}
}

func TestExternalManualExecutor_RemainsCanonicalOnly(t *testing.T) {
	manual := &canonicalOnlyExecutor{}
	var view lipsdk.ExecutorView = manual
	if view == nil {
		t.Fatal("manual executor must satisfy lipsdk.ExecutorView")
	}
	if _, ok := largebody.AsLargeBodyExecutor(view); ok {
		t.Fatal("external/manual executor without capability must stay canonical-only")
	}
	stream, err := view.Execute(context.Background(), &lipapi.Call{})
	if err != nil {
		t.Fatalf("canonical Execute error = %v", err)
	}
	if stream == nil {
		t.Fatal("canonical Execute must return a stream")
	}
	if manual.executeCalls != 1 {
		t.Fatalf("canonical Execute calls = %d, want 1", manual.executeCalls)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("stream Close error = %v", err)
	}
}
