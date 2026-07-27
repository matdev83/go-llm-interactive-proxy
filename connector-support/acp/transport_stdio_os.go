package acp

import (
	"fmt"
	"io"
	"os/exec"
)

// osProcess implements Process using exec.Cmd with stdin/stdout pipes.
type osProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

// OSProcessStarter implements ProcessStarter using os/exec.
type OSProcessStarter struct{}

// Start spawns a subprocess with stdin/stdout/stderr pipes. On Windows it sets
// CREATE_NEW_PROCESS_GROUP so taskkill /T can cleanly terminate the process tree.
func (OSProcessStarter) Start(cmdArgs []string, cwd string, env []string) (Process, error) {
	if len(cmdArgs) == 0 {
		return nil, fmt.Errorf("acp: empty command")
	}
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if len(env) > 0 {
		cmd.Env = env
	}
	setProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("acp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("acp: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("acp: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("acp: start process: %w", err)
	}
	return &osProcess{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

func (p *osProcess) PID() int              { return p.cmd.Process.Pid }
func (p *osProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *osProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *osProcess) Stderr() io.ReadCloser { return p.stderr }

func (p *osProcess) Wait() error {
	return p.cmd.Wait()
}

func (p *osProcess) Kill() error {
	return killProcessTree(p.cmd)
}
