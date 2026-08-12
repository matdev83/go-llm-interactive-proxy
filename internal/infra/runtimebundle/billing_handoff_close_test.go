package runtimebundle_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBuildExecutorQuiesceBoundsBillingHandoffWait(t *testing.T) {
	t.Parallel()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "build_executor.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if !strings.Contains(text, "WaitBillingHandoffRetriesForClose") {
		t.Fatal("PhaseQuiesce billing-handoff closer must bound wait with WaitBillingHandoffRetriesForClose")
	}
	if strings.Contains(text, "exec.WaitBillingHandoffRetries()") {
		t.Fatal("PhaseQuiesce billing-handoff closer must not wait unbounded on WaitBillingHandoffRetries")
	}
}
