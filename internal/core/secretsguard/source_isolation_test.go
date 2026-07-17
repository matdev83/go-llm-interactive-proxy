package secretsguard_test

import (
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/secretsguard"
)

type panicEnvironment struct {
	calls int
}

func (p *panicEnvironment) Lookup(string) (string, bool) {
	p.calls++
	panic("secretsguard: multi_user must never call Environment.Lookup")
}

func (p *panicEnvironment) Snapshot() []string {
	p.calls++
	panic("secretsguard: multi_user must never call Environment.Snapshot")
}

func TestNewMultiUserSource_zeroEnvironmentCalls(t *testing.T) {
	t.Parallel()
	env := &panicEnvironment{}
	src, err := secretsguard.NewMultiUserSource(env)
	if err != nil {
		t.Fatal(err)
	}
	if src.AccessMode() != accessmode.ModeMultiUser {
		t.Fatalf("access mode: got %q", src.AccessMode())
	}
	if env.calls != 0 {
		t.Fatalf("Environment calls: got %d want 0", env.calls)
	}
	_ = src.EntryCount()
	_ = src.MatcherResolver()
	if env.calls != 0 {
		t.Fatalf("Environment calls after inspect: got %d want 0", env.calls)
	}
}

func TestNewDisabledSource_zeroEnvironmentCalls(t *testing.T) {
	t.Parallel()
	env := &panicEnvironment{}
	src := secretsguard.NewDisabledSource()
	_ = src.EntryCount()
	_ = src.MatcherResolver()
	_ = src.AccessMode()
	if env.calls != 0 {
		t.Fatalf("disabled source must not call Environment; calls=%d", env.calls)
	}
}

func TestSource_hasNoRawCatalogAccessor(t *testing.T) {
	t.Parallel()
	rt := reflect.TypeFor[secretsguard.Source]()
	for m := range rt.Methods() {
		switch m.Name {
		case "Values", "Secrets", "Catalog", "Env", "RawSecrets":
			t.Fatalf("Source must not expose raw accessor %q", m.Name)
		}
	}
	src, err := secretsguard.NewMultiUserSource(nil)
	if err != nil {
		t.Fatal(err)
	}
	var _ secretsguard.Source = src
}
