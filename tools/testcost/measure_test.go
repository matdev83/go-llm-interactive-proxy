package testcost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/tools/taskrunner"
)

type recordingRunner struct {
	result taskrunner.Result
	run    func(taskrunner.Request)
}

func (r *recordingRunner) Run(_ context.Context, request taskrunner.Request) taskrunner.Result {
	if r.run != nil {
		r.run(request)
	}
	return r.result
}

func TestResolveParallelUsesPositiveEnvOrLogicalCPU(t *testing.T) {
	if got := ResolveParallel("9", 4); got != 9 {
		t.Fatalf("ResolveParallel positive = %d", got)
	}
	if got := ResolveParallel("0", 4); got != 4 {
		t.Fatalf("ResolveParallel zero = %d", got)
	}
	if got := ResolveParallel("bad", 4); got != 4 {
		t.Fatalf("ResolveParallel malformed = %d", got)
	}
	if got := ResolveParallel("", 0); got != 1 {
		t.Fatalf("ResolveParallel zero CPU = %d", got)
	}
}

func TestBuildTestUnitRequestExactCommand(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("test-unit measurement is Windows-only")
	}
	request, err := BuildTestUnitRequest(MeasureOptions{Root: t.TempDir(), Parallel: 7})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"go", "test", "-count=1", "-json", "-parallel=7", "-timeout=10m", "./..."}
	if !reflect.DeepEqual(request.Argv, want) {
		t.Fatalf("Argv = %#v, want %#v", request.Argv, want)
	}
	if request.Timeout != 12*time.Minute || request.Output != taskrunner.Stream {
		t.Fatalf("timeout/output = %s/%v", request.Timeout, request.Output)
	}
}

func TestBuildQualityChecksRequestExactEnvironment(t *testing.T) {
	if runtime.GOOS != "windows" {
		if _, err := BuildQualityChecksRequest(MeasureOptions{Root: t.TempDir(), TempRoot: t.TempDir(), Parallel: 3}); !errors.Is(err, ErrWindowsOnly) {
			t.Fatalf("error = %v, want ErrWindowsOnly", err)
		}
		return
	}
	root := t.TempDir()
	tempRoot := t.TempDir()
	request, err := BuildQualityChecksRequest(MeasureOptions{Root: root, TempRoot: tempRoot, Parallel: 3})
	if err != nil {
		t.Fatal(err)
	}
	wantArgv := []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "scripts/quality-checks.ps1"}
	wantEnv := []string{"CI=", "LIP_VERIFY_MODULE_CACHE=", "LIP_SKIP_ARCHTEST=", "LIP_SKIP_GO_COMPILE_CHECKS=", "LIP_TEST_PARALLEL=3", "TEMP=" + tempRoot, "TMP=" + tempRoot}
	if !reflect.DeepEqual(request.Argv, wantArgv) || !reflect.DeepEqual(request.Env, wantEnv) || request.ClearEnv {
		t.Fatalf("quality request argv/env/clear = %#v/%#v/%v", request.Argv, request.Env, request.ClearEnv)
	}
}

func TestMeasureStreamsLogsAndMapsAccountingWithoutPackageWallSum(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("measurement is Windows-only")
	}
	root := t.TempDir()
	tempRoot := t.TempDir()
	runner := &recordingRunner{result: taskrunner.Result{Kind: taskrunner.Success, Elapsed: time.Second, Accounting: taskrunner.ProcessAccounting{
		Supported: true, UserCPUNanos: 1, KernelCPUNanos: 2, TotalCPUNanos: 3, TotalProcesses: 4, ActiveProcesses: 5, TerminatedProcesses: 6, PageFaults: 7,
		ReadOperations: 8, WriteOperations: 9, OtherOperations: 10, ReadBytes: 11, WriteBytes: 12, OtherBytes: 13,
	}}}
	runner.run = func(request taskrunner.Request) {
		_, _ = request.StreamOut.Write([]byte(`{"Action":"pass","Package":"pkg","Elapsed":9}` + "\n" + `{"Action":"pass","Package":"pkg","Elapsed":1.25}` + "\n"))
		_, _ = request.StreamErr.Write([]byte("diagnostic\n"))
	}
	measurement, err := Measure(context.Background(), TargetTestUnit, MeasureOptions{Root: root, TempRoot: tempRoot, Revision: "r", Parallel: 2, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if measurement.WallNanos != uint64(time.Second) {
		t.Fatalf("wall_nanos = %d, want process wall %d", measurement.WallNanos, time.Second)
	}
	if measurement.Packages["pkg"].ElapsedNanos != 1_250_000_000 {
		t.Fatalf("package elapsed = %#v", measurement.Packages)
	}
	if measurement.Process.UserCPUNanos != 1 || measurement.Process.OtherBytes != 13 {
		t.Fatalf("process metrics = %#v", measurement.Process)
	}
	if _, err := os.Stat(measurement.StdoutLog); err != nil {
		t.Fatalf("stdout log: %v", err)
	}
	if _, err := os.Stat(measurement.StderrLog); err != nil {
		t.Fatalf("stderr log: %v", err)
	}
	if !strings.HasPrefix(filepath.Clean(measurement.StdoutLog), filepath.Clean(tempRoot)+string(os.PathSeparator)) {
		t.Fatalf("stdout log escaped temp root: %s", measurement.StdoutLog)
	}
}

func TestMeasureAccountingAndFailureTailFailClosed(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("measurement is Windows-only")
	}
	for _, tc := range []struct {
		name   string
		result taskrunner.Result
		want   error
	}{
		{name: "unsupported", result: taskrunner.Result{Kind: taskrunner.Success}, want: ErrAccountingUnsupported},
		{name: "error", result: taskrunner.Result{Kind: taskrunner.Success, AccountingErr: errors.New("accounting query")}, want: ErrAccountingFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &recordingRunner{result: tc.result, run: func(request taskrunner.Request) { _, _ = request.StreamErr.Write([]byte(strings.Repeat("x", 100))) }}
			_, err := Measure(context.Background(), TargetTestUnit, MeasureOptions{Root: t.TempDir(), TempRoot: t.TempDir(), Runner: runner})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Measure() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestMeasureQualityChecksOmitsPackages(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("measurement is Windows-only")
	}
	runner := &recordingRunner{result: taskrunner.Result{Kind: taskrunner.Success, Elapsed: time.Second, Accounting: taskrunner.ProcessAccounting{Supported: true}}}
	measurement, err := Measure(context.Background(), TargetQualityChecks, MeasureOptions{Root: t.TempDir(), TempRoot: t.TempDir(), Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if measurement.Packages != nil {
		t.Fatalf("quality-checks packages = %#v, want nil", measurement.Packages)
	}
}
