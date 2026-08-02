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
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return "", os.ErrNotExist
	}
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return "", err
	}
	if exitCode != windowsStillActive {
		return "", os.ErrNotExist
	}
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

// InspectWindowsProcess reads the process identity, image path, and liveness
// from one kernel handle. Keeping one handle closes the PID-reuse race that
// would exist if identity and image were inspected in separate calls.
func InspectWindowsProcess(pid int) (identity string, command string, running bool, err error) {
	if pid < 1 {
		return "", "", false, nil
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return "", "", false, err
	}
	if exitCode != windowsStillActive {
		return "", "", false, nil
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return "", "", false, err
	}
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		// The process may have exited after the first liveness check. Report that
		// as stopped instead of turning an ordinary shutdown race into an error.
		if exitErr := windows.GetExitCodeProcess(handle, &exitCode); exitErr == nil && exitCode != windowsStillActive {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	if size < 1 || size > uint32(len(buffer)) {
		return "", "", false, ErrUnsafe
	}
	return fmt.Sprintf("%08x:%08x", created.HighDateTime, created.LowDateTime),
		windows.UTF16ToString(buffer[:size]), true, nil
}

// WindowsProcessCommand returns the kernel-reported executable image path.
// The process creation time remains the separate anti-PID-reuse authority.
func WindowsProcessCommand(pid int) (string, error) {
	_, command, running, err := InspectWindowsProcess(pid)
	if err != nil {
		return "", err
	}
	if !running {
		return "", os.ErrNotExist
	}
	return command, nil
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
