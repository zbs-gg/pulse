//go:build !windows

package historicalingest

import (
	"os"
	"syscall"
)

func hasMultipleLinksInfo(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink > 1
}

func hasMultipleLinksFile(_ *os.File, info os.FileInfo) bool {
	return hasMultipleLinksInfo(info)
}

func codexFileIdentity(_ *os.File, info os.FileInfo) (uint64, uint64) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	return uint64(stat.Dev), uint64(stat.Ino)
}
