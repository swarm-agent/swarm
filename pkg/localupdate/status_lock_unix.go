//go:build unix

package localupdate

import (
	"os"
	"syscall"
)

func lockUpdateStatusFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

func unlockUpdateStatusFile(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
