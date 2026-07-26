//go:build linux

package processhost

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/trust"
	"golang.org/x/sys/unix"
)

// Launch uses a duplicated held descriptor via /proc/self/fd/%d so the caller
// may Close the VerifiedArtifact without affecting the generation. Shell is
// never used. Env must already be allowlisted by the supervisor.
func (PlatformLauncher) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	if spec.Artifact == nil || spec.Artifact.OpenFile() == nil {
		return nil, ReasonArtifactRequired
	}
	if spec.Artifact.Strategy != trust.BindingDescriptor {
		return nil, ReasonUnsupportedBinding
	}
	src := spec.Artifact.OpenFile()
	dupFD, err := unix.Dup(int(src.Fd()))
	if err != nil {
		return nil, fmt.Errorf("%w: dup: %v", ReasonLaunchFailed, err)
	}
	unix.CloseOnExec(dupFD)
	held := os.NewFile(uintptr(dupFD), "lip-bp-launch")
	if held == nil {
		_ = unix.Close(dupFD)
		return nil, fmt.Errorf("%w: newfile", ReasonLaunchFailed)
	}
	fdPath := "/proc/self/fd/" + strconv.Itoa(int(held.Fd()))
	cmd := exec.CommandContext(ctx, fdPath)
	cmd.Dir = spec.WorkDir
	cmd.Env = append([]string{}, spec.Env...)
	cmd.ExtraFiles = append([]*os.File{}, spec.ExtraFiles...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = held.Close()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = held.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = held.Close()
		return nil, fmt.Errorf("%w: %v", ReasonLaunchFailed, err)
	}
	return &linuxProc{
		cmd: cmd, gen: spec.Generation, stdout: stdout, stderr: stderr,
		pgid: cmd.Process.Pid, held: held,
	}, nil
}

type linuxProc struct {
	cmd    *exec.Cmd
	gen    uint64
	stdout io.ReadCloser
	stderr io.ReadCloser
	pgid   int
	held   *os.File
	waited bool
	err    error
}

func (p *linuxProc) PID() int                 { return p.cmd.Process.Pid }
func (p *linuxProc) Generation() uint64       { return p.gen }
func (p *linuxProc) Stdout() io.ReadCloser    { return p.stdout }
func (p *linuxProc) Stderr() io.ReadCloser    { return p.stderr }
func (p *linuxProc) ContainsPID(pid int) bool { return pid == p.PID() }
func (p *linuxProc) SignalKill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return unix.Kill(-p.pgid, unix.SIGKILL)
}
func (p *linuxProc) GracefulStop(timeout time.Duration) error {
	if p.cmd.Process == nil {
		return nil
	}
	_ = unix.Kill(-p.pgid, unix.SIGTERM)
	done := make(chan struct{})
	go func() {
		_ = p.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return p.err
	case <-timer.C:
		_ = p.SignalKill()
		<-done
		return p.err
	}
}
func (p *linuxProc) Wait() error {
	if p.waited {
		return p.err
	}
	p.waited = true
	p.err = p.cmd.Wait()
	return p.err
}
func (p *linuxProc) Close() error {
	_ = p.SignalKill()
	err := p.Wait()
	if p.held != nil {
		_ = p.held.Close()
		p.held = nil
	}
	return err
}
