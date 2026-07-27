//go:build windows

package processhost

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/trust"
	"golang.org/x/sys/windows"
)

// Launch binds CreateProcess to the held protected-staging file identity.
// A duplicated FILE_SHARE_READ launch handle is owned by the process for the
// generation lifetime so the caller may Close the VerifiedArtifact immediately.
// The child starts suspended, joins a KILL_ON_JOB_CLOSE job, has its image
// FileId verified against the generation-owned handle, then resumes.
func (PlatformLauncher) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	_ = ctx
	if spec.Artifact == nil || spec.Artifact.OpenFile() == nil {
		return nil, ReasonArtifactRequired
	}
	if spec.Artifact.Strategy != trust.BindingProtectedStaging || spec.Artifact.StagedPath == "" {
		return nil, ReasonUnsupportedBinding
	}
	src := spec.Artifact.OpenFile()
	held, err := duplicateLaunchFile(src, spec.Artifact.StagedPath)
	if err != nil {
		return nil, fmt.Errorf("%w: dup launch handle: %v", ReasonLaunchFailed, err)
	}
	heldID, err := trust.FileIdentityFromOSFile(held.Fd())
	if err != nil {
		_ = held.Close()
		return nil, fmt.Errorf("%w: %v", ReasonUnsupportedBinding, err)
	}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		_ = held.Close()
		return nil, fmt.Errorf("%w: job: %v", ReasonLaunchFailed, err)
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		_ = held.Close()
		return nil, fmt.Errorf("%w: job limit: %v", ReasonLaunchFailed, err)
	}

	app, err := windows.UTF16PtrFromString(spec.Artifact.StagedPath)
	if err != nil {
		_ = windows.CloseHandle(job)
		_ = held.Close()
		return nil, err
	}
	var dir *uint16
	if spec.WorkDir != "" {
		dir, err = windows.UTF16PtrFromString(spec.WorkDir)
		if err != nil {
			_ = windows.CloseHandle(job)
			_ = held.Close()
			return nil, err
		}
	}
	envBlock, err := createEnvBlock(withWindowsRuntimeEnv(spec.Env))
	if err != nil {
		_ = windows.CloseHandle(job)
		_ = held.Close()
		return nil, err
	}

	var si windows.StartupInfo
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi windows.ProcessInformation
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NEW_PROCESS_GROUP)
	err = windows.CreateProcess(
		app,
		nil,
		nil,
		nil,
		false,
		flags,
		envBlock,
		dir,
		&si,
		&pi,
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		_ = held.Close()
		return nil, fmt.Errorf("%w: CreateProcess: %v", ReasonLaunchFailed, err)
	}

	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = windows.CloseHandle(pi.Thread)
		_ = windows.CloseHandle(pi.Process)
		_ = windows.CloseHandle(job)
		_ = held.Close()
		return nil, fmt.Errorf("%w: assign job: %v", ReasonLaunchFailed, err)
	}

	// Pathname only locates the child-reported image; identity proof is FileId
	// equality against the generation-owned share-locked handle (never a path hash).
	imgID, imgPath, err := processImageIdentity(pi.Process)
	if err != nil || imgID != heldID {
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = windows.CloseHandle(pi.Thread)
		_ = windows.CloseHandle(pi.Process)
		_ = windows.CloseHandle(job)
		_ = held.Close()
		return nil, fmt.Errorf("%w: image identity mismatch path=%q", ReasonSubstitution, imgPath)
	}

	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = windows.CloseHandle(pi.Thread)
		_ = windows.CloseHandle(pi.Process)
		_ = windows.CloseHandle(job)
		_ = held.Close()
		return nil, fmt.Errorf("%w: resume: %v", ReasonLaunchFailed, err)
	}
	_ = windows.CloseHandle(pi.Thread)

	return &windowsProc{
		proc:   pi.Process,
		job:    job,
		pid:    int(pi.ProcessId),
		gen:    spec.Generation,
		held:   held,
		heldID: heldID,
		staged: spec.Artifact.StagedPath,
		stdout: io.NopCloser(strings.NewReader("")),
		stderr: io.NopCloser(strings.NewReader("")),
	}, nil
}

func duplicateLaunchFile(src *os.File, name string) (*os.File, error) {
	var dup windows.Handle
	err := windows.DuplicateHandle(
		windows.CurrentProcess(),
		windows.Handle(src.Fd()),
		windows.CurrentProcess(),
		&dup,
		0,
		false,
		windows.DUPLICATE_SAME_ACCESS,
	)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dup), name)
	if f == nil {
		_ = windows.CloseHandle(dup)
		return nil, fmt.Errorf("newfile")
	}
	return f, nil
}

type windowsProc struct {
	proc   windows.Handle
	job    windows.Handle
	pid    int
	gen    uint64
	held   *os.File
	heldID trust.FileIdentity
	staged string

	stdout io.ReadCloser
	stderr io.ReadCloser

	waitOnce sync.Once
	waitErr  error
	mu       sync.Mutex
	closed   bool
}

func (p *windowsProc) PID() int              { return p.pid }
func (p *windowsProc) Generation() uint64    { return p.gen }
func (p *windowsProc) Stdout() io.ReadCloser { return p.stdout }
func (p *windowsProc) Stderr() io.ReadCloser { return p.stderr }
func (p *windowsProc) ContainsPID(pid int) bool {
	if p.job == 0 || pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var inJob bool
	if err := isProcessInJob(h, p.job, &inJob); err != nil {
		return false
	}
	return inJob
}

func (p *windowsProc) SignalKill() error {
	return windows.TerminateProcess(p.proc, 1)
}

func (p *windowsProc) GracefulStop(timeout time.Duration) error {
	_ = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(p.pid))
	done := make(chan struct{})
	go func() {
		_ = p.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return p.waitErr
	case <-timer.C:
		_ = p.SignalKill()
		<-done
		return p.waitErr
	}
}

func (p *windowsProc) Wait() error {
	p.waitOnce.Do(func() {
		s, err := windows.WaitForSingleObject(p.proc, windows.INFINITE)
		if err != nil {
			p.waitErr = err
			return
		}
		if s != windows.WAIT_OBJECT_0 {
			p.waitErr = fmt.Errorf("wait status %d", s)
			return
		}
		var code uint32
		if err := windows.GetExitCodeProcess(p.proc, &code); err != nil {
			p.waitErr = err
			return
		}
		if code != 0 {
			p.waitErr = fmt.Errorf("exit %d", code)
		}
	})
	return p.waitErr
}

func (p *windowsProc) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	_ = p.SignalKill()
	err := p.Wait()
	if p.job != 0 {
		_ = windows.CloseHandle(p.job)
		p.job = 0
	}
	if p.proc != 0 {
		_ = windows.CloseHandle(p.proc)
		p.proc = 0
	}
	if p.held != nil {
		_ = p.held.Close()
		p.held = nil
	}
	return err
}

func (p *windowsProc) JobHandle() windows.Handle { return p.job }
func (p *windowsProc) HeldIdentity() trust.FileIdentity {
	return p.heldID
}

func withWindowsRuntimeEnv(env []string) []string {
	need := []string{"SystemRoot", "SYSTEMROOT", "SystemDrive", "WINDIR", "ComSpec", "PATHEXT", "TEMP", "TMP", "PATH", "Path"}
	have := map[string]struct{}{}
	for _, e := range env {
		if k, _, ok := strings.Cut(e, "="); ok {
			have[strings.ToUpper(k)] = struct{}{}
		}
	}
	out := append([]string{}, env...)
	for _, k := range need {
		if _, ok := have[strings.ToUpper(k)]; ok {
			continue
		}
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
			have[strings.ToUpper(k)] = struct{}{}
		}
	}
	return out
}

func createEnvBlock(env []string) (*uint16, error) {
	if len(env) == 0 {
		b := []uint16{0}
		return &b[0], nil
	}
	var b []uint16
	for _, e := range env {
		u, err := syscall.UTF16FromString(e)
		if err != nil {
			return nil, err
		}
		b = append(b, u...)
	}
	b = append(b, 0)
	return &b[0], nil
}

// processImageIdentity opens the child-reported image path only to read its
// ByHandle FileId. The path string is not hashed and is not identity proof;
// callers compare FileId to the generation-owned held handle under FILE_SHARE_READ.
func processImageIdentity(proc windows.Handle) (trust.FileIdentity, string, error) {
	var buf [windows.MAX_PATH]uint16
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(proc, 0, &buf[0], &size); err != nil {
		return trust.FileIdentity{}, "", err
	}
	path := windows.UTF16ToString(buf[:size])
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return trust.FileIdentity{}, path, err
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return trust.FileIdentity{}, path, err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	id, err := trust.FileIdentityFromHandle(h)
	return id, path, err
}

var (
	modKernel32        = windows.NewLazySystemDLL("kernel32.dll")
	procIsProcessInJob = modKernel32.NewProc("IsProcessInJob")
)

func isProcessInJob(process, job windows.Handle, result *bool) error {
	var r uint32
	ret, _, err := procIsProcessInJob.Call(uintptr(process), uintptr(job), uintptr(unsafe.Pointer(&r)))
	if ret == 0 {
		return err
	}
	*result = r != 0
	return nil
}
