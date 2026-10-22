//go:build !linux && !darwin

package main

import "io"

// Hidden terminal entry is supported on Linux and macOS; redirected input
// retains the existing stdin-only contract on other platforms.
func readSecretValue(input io.Reader, _ string) ([]byte, error) {
	return readStdinSecret(input)
}
