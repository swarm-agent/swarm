//go:build unix

package appstorage

import (
	"os"
	"syscall"
)

// OwnedByAnother reports whether path belongs to a user other than this
// process (a person's folder made visible to the daemon).
func OwnedByAnother(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) != os.Geteuid()
}
