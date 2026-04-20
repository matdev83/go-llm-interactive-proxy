// Package refclienttest holds helpers shared by refclient emulator tests.
package refclienttest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// ModuleRoot returns the directory containing go.mod.
func ModuleRoot(tb testing.TB) string {
	tb.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		tb.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 16; i++ {
		mod := filepath.Join(dir, "go.mod")
		if _, err := os.Stat(mod); err == nil {
			return dir
		}
		next := filepath.Dir(dir)
		if next == dir {
			break
		}
		dir = next
	}
	tb.Fatal("go.mod not found walking up from", filepath.Dir(file))
	return ""
}

// ReadRefclientFixture loads testdata/refclient/<name> from the module root.
func ReadRefclientFixture(tb testing.TB, name string) []byte {
	tb.Helper()
	p := filepath.Join(ModuleRoot(tb), "testdata", "refclient", name)
	b, err := os.ReadFile(p)
	if err != nil {
		tb.Fatalf("read fixture %s: %v", p, err)
	}
	return b
}
