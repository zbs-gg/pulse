//go:build linux

package platform

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func ProcessIdentity(pid int) (string, error) {
	if pid < 1 {
		return "", os.ErrNotExist
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	closing := strings.LastIndexByte(string(raw), ')')
	if closing < 0 {
		return "", ErrUnsafe
	}
	fields := strings.Fields(string(raw[closing+1:]))
	if len(fields) < 20 {
		return "", ErrUnsafe
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", ErrUnsafe
	}
	return fields[19], nil
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
