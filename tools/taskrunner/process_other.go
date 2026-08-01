//go:build !windows && !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package taskrunner

import (
	"os/exec"
)

type otherProcess struct{ cmd *exec.Cmd }

func newPlatformProcessAdapter(cmd *exec.Cmd) (processAdapter, error) {
	return &otherProcess{cmd: cmd}, nil
}
func (p *otherProcess) start() error               { return p.cmd.Start() }
func (p *otherProcess) startupCleanupError() error { return nil }
func (p *otherProcess) kill() error {
	if p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}
func (p *otherProcess) close() error { return nil }
