//go:build !unix

package sandbox

// fileOwnerUID is unavailable off Unix: no safe.directory is added.
func fileOwnerUID(string) (int, bool) { return 0, false }
