// Package projectfiles defines creation modes for user-owned project output.
// Let inherited default ACLs (or the process umask) decide access. Never use
// these modes for daemon-private storage or chmod existing entries to them.
package projectfiles

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
)

const (
	DirMode  os.FileMode = 0o777
	FileMode os.FileMode = 0o666
)

// CreateTemp creates an exclusive project-output staging file. os.CreateTemp
// requests 0600, which would mask inherited named-user ACL permissions.
func CreateTemp(dir, prefix string) (*os.File, error) {
	for i := 0; i < 10; i++ {
		var suffix [16]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(filepath.Join(dir, prefix+hex.EncodeToString(suffix[:])), os.O_RDWR|os.O_CREATE|os.O_EXCL, FileMode)
		if os.IsExist(err) {
			continue
		}
		return file, err
	}
	return nil, os.ErrExist
}
