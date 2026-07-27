//go:build windows

package historicalingest

import (
	"os"
	"syscall"
)

// Windows FileInfo does not expose link count or the stable file index. Read
// both from the already-open handle so the source cannot be swapped between
// path validation and version capture.
func windowsCodexFileInformation(file *os.File) (syscall.ByHandleFileInformation, bool) {
	var info syscall.ByHandleFileInformation
	if file == nil || syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info) != nil {
		return syscall.ByHandleFileInformation{}, false
	}
	return info, true
}

func hasMultipleLinksInfo(_ os.FileInfo) bool {
	// The authoritative check happens after opening the exact file handle.
	return false
}

func hasMultipleLinksFile(file *os.File, _ os.FileInfo) bool {
	info, ok := windowsCodexFileInformation(file)
	return !ok || info.NumberOfLinks > 1
}

func codexFileIdentity(file *os.File, _ os.FileInfo) (uint64, uint64) {
	info, ok := windowsCodexFileInformation(file)
	if !ok {
		return 0, 0
	}
	fileIndex := uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	return uint64(info.VolumeSerialNumber), fileIndex
}
