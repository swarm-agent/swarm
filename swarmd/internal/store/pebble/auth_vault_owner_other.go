//go:build !unix

package pebblestore

import "os"

func localRootKeyOwnedByCurrentUser(info os.FileInfo) bool {
	return false
}

func openLocalRootKey(path string) (*os.File, error) {
	return os.Open(path)
}
