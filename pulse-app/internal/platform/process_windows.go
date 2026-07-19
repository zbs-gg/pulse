//go:build windows

package platform

import (
	"errors"
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

// WindowsProcessCommand returns the kernel-reported executable image path.
// The process creation time remains the separate anti-PID-reuse authority.
func WindowsProcessCommand(pid int) (string, error) {
	if pid < 1 {
		return "", os.ErrNotExist
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	if size < 1 || size > uint32(len(buffer)) {
		return "", ErrUnsafe
	}
	return windows.UTF16ToString(buffer[:size]), nil
}

// TerminateWindowsProcess terminates the process only when the creation-time
// identity still matches on the same kernel handle, preventing PID reuse.
func TerminateWindowsProcess(pid int, identity string) (bool, error) {
	if pid < 1 || identity == "" {
		return false, os.ErrNotExist
	}
	handle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(pid),
	)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(handle)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return false, err
	}
	if identity != fmt.Sprintf("%08x:%08x", created.HighDateTime, created.LowDateTime) {
		return false, ErrUnsafe
	}
	if err := windows.TerminateProcess(handle, 1); err != nil {
		return false, err
	}
	return true, nil
}
