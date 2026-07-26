//go:build !unix

package startupconfig

import "os"

func fileOwnership(os.FileInfo) (int, int) {
	return -1, -1
}
