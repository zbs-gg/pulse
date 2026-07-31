//go:build windows

package platform

import "os"

func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
