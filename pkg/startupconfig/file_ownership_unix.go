//go:build unix

package startupconfig

import (
	"os"
	"syscall"
)

func fileOwnership(info os.FileInfo) (int, int) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, -1
	}
	return int(stat.Uid), int(stat.Gid)
}
