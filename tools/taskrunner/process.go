package taskrunner

import "os/exec"

type processAdapter interface {
	start() error
	startupCleanupError() error
	kill() error
	close() error
}

func newProcessAdapter(cmd *exec.Cmd) (processAdapter, error) { return newPlatformProcessAdapter(cmd) }
