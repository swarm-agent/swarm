//go:build !linux

package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// Design file writes need per-component no-follow opens (openat with
// O_NOFOLLOW). Other platforms refuse instead of following symlinks.
var errDesignFilesUnsupported = errors.New("design files require Linux no-follow file operations")

func designDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func designReadNoFollow(*rootedWorkspacePath) ([]byte, error) { return nil, errDesignFilesUnsupported }

func designCreateExclusive(*rootedWorkspacePath, []byte) error { return errDesignFilesUnsupported }

func designReplace(*rootedWorkspacePath, string, []byte) error { return errDesignFilesUnsupported }
