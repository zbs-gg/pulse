//go:build windows

package platform

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

const windowsStillActive = 259

func ProcessIdentity(pid int) (string, error) {
	if pid < 1 {
		return "", os.ErrNotExist
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return "", err
	}
	return fmt.Sprintf("%08x:%08x", created.HighDateTime, created.LowDateTime), nil
}

func ProcessAlive(pid int, identity string) bool {
	if pid < 1 || identity == "" {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil || exitCode != windowsStillActive {
		return false
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return false
	}
	return identity == fmt.Sprintf("%08x:%08x", created.HighDateTime, created.LowDateTime)
}
