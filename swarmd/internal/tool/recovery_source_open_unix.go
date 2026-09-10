//go:build unix

package tool

import (
	"golang.org/x/sys/unix"
	"os"
)

// Nonblocking and no-follow prevent a replaced FIFO from hanging capture and
// a replaced final symlink from changing the selected source at open time.
func openRecoverySourceFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
}
