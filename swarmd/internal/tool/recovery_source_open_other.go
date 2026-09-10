//go:build !unix

package tool

import (
	"errors"
	"os"
)

func openRecoverySourceFile(root *os.Root, name string) (*os.File, error) {
	return nil, errors.New("safe recovery source capture is unsupported on this platform")
}
