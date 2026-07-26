package cursorsdk

import (
	"fmt"
	"io"
	"os/exec"
)

// Process abstracts a bridge subprocess. Tests inject fakes; production uses OSProcessStarter.
type Process interface {
	PID() int
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait() error
	Kill() error
}

// ProcessStarter spawns a Process from argv, working directory, and environment.
type ProcessStarter interface {
	Start(cmd []string, cwd string, env []string) (Process, error)
}

type osProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

// OSProcessStarter implements ProcessStarter using os/exec with process-group isolation.
type OSProcessStarter struct{}

func (OSProcessStarter) Start(cmdArgs []string, cwd string, env []string) (Process, error) {
	if len(cmdArgs) == 0 {
		return nil, fmt.Errorf("cursorsdk: empty bridge command")
	}
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if env != nil {
		cmd.Env = env
	}
	setProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("cursorsdk: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("cursorsdk: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("cursorsdk: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("cursorsdk: start bridge: %w", err)
	}
	return &osProcess{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

func (p *osProcess) PID() int              { return p.cmd.Process.Pid }
func (p *osProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *osProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *osProcess) Stderr() io.ReadCloser { return p.stderr }

func (p *osProcess) Wait() error { return p.cmd.Wait() }

func (p *osProcess) Kill() error { return killProcessTree(p.cmd) }

// killProcessHandle kills only the owned process handle (no process-group/tree kill).
// Used when identity checks fail so PID-reuse cannot widen the blast radius.
func killProcessHandle(proc Process) error {
	if proc == nil {
		return nil
	}
	if op, ok := proc.(*osProcess); ok {
		if op.cmd.Process == nil {
			return nil
		}
		return op.cmd.Process.Kill()
	}
	return proc.Kill()
}
