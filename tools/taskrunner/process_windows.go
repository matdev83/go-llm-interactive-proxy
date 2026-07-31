//go:build windows

package taskrunner

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProcess struct {
	cmd      *exec.Cmd
	job      windows.Handle
	process  windows.Handle
	assigned bool
	startErr error
	closed   bool
	killOnce sync.Once
	killErr  error
}

func newPlatformProcessAdapter(cmd *exec.Cmd) (processAdapter, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("configure job object: %w", err)
	}
	return &windowsProcess{cmd: cmd, job: job}, nil
}

func (p *windowsProcess) start() error {
	if err := p.cmd.Start(); err != nil {
		return err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(p.cmd.Process.Pid))
	if err != nil {
		p.startErr = fmt.Errorf("open child process: %w", err)
		return nil
	}
	p.process = handle
	if err := windows.AssignProcessToJobObject(p.job, handle); err != nil {
		var code uint32
		if exitErr := windows.GetExitCodeProcess(handle, &code); exitErr == nil && code == stillActive {
			p.startErr = fmt.Errorf("assign process to job object: %w", err)
			p.startErr = errors.Join(p.startErr, p.killDirect())
			return nil
		}
		// A process that exited before assignment needs only the normal Wait.
		return nil
	}
	p.assigned = true
	return nil
}

func (p *windowsProcess) startupCleanupError() error { return p.startErr }
func (p *windowsProcess) killDirect() error {
	if p.process != 0 {
		state, err := windows.WaitForSingleObject(p.process, 0)
		if err != nil {
			return err
		}
		if state == windows.WAIT_OBJECT_0 {
			return nil
		}
		return windows.TerminateProcess(p.process, 1)
	}
	if p.cmd.Process == nil || p.cmd.ProcessState != nil {
		return nil
	}
	return p.cmd.Process.Kill()
}
func (p *windowsProcess) kill() error {
	p.killOnce.Do(func() {
		if p.assigned {
			p.killErr = windows.TerminateJobObject(p.job, 1)
			return
		}
		p.killErr = p.killDirect()
	})
	return p.killErr
}
func (p *windowsProcess) close() error {
	if p.closed {
		return nil
	}
	p.closed = true
	var err error
	if p.process != 0 {
		err = windows.CloseHandle(p.process)
	}
	if closeErr := windows.CloseHandle(p.job); err == nil {
		err = closeErr
	}
	return err
}

const stillActive = 259
