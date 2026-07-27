//go:build !unix

package localupdate

import "os"

func lockUpdateStatusFile(_ *os.File) error {
	return nil
}

func unlockUpdateStatusFile(_ *os.File) {}
