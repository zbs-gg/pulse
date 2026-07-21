//go:build !windows

package platform

import (
	"os"
	"syscall"
)

func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
