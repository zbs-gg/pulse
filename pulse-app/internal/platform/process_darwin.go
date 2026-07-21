//go:build darwin

package platform

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func ProcessIdentity(pid int) (string, error) {
	if pid < 1 {
		return "", os.ErrNotExist
	}
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if process == nil || int(process.Proc.P_pid) != pid {
		return "", os.ErrNotExist
	}
	started := process.Proc.P_starttime
	return fmt.Sprintf("%d:%d", started.Sec, started.Usec), nil
}

func ProcessAlive(pid int, identity string) bool {
	if pid < 1 || identity == "" {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	current, err := ProcessIdentity(pid)
	return err == nil && current == identity
}
