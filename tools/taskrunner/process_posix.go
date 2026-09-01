//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package taskrunner

import (
	"os/exec"
	"syscall"
)

type posixProcess struct{ cmd *exec.Cmd }

func newPlatformProcessAdapter(cmd *exec.Cmd, _ bool) (processAdapter, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &posixProcess{cmd: cmd}, nil
}

func (p *posixProcess) start() error               { return p.cmd.Start() }
func (p *posixProcess) startupCleanupError() error { return nil }
func (p *posixProcess) accounting() (ProcessAccounting, error) {
	return ProcessAccounting{Supported: false}, nil
}
func (p *posixProcess) kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
}
func (p *posixProcess) close() error { return nil }
