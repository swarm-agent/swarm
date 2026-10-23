//go:build linux || darwin

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	"golang.org/x/sys/unix"
)

func readSecretValue(input io.Reader, name string) (value []byte, err error) {
	file, ok := input.(*os.File)
	if !ok {
		return readStdinSecret(input)
	}
	fd := int(file.Fd())
	original, ioctlErr := unix.IoctlGetTermios(fd, secretGetTermios)
	if ioctlErr != nil {
		if errors.Is(ioctlErr, unix.ENOTTY) {
			return readStdinSecret(input)
		}
		return nil, errors.New("cannot inspect secret input terminal")
	}
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, unix.SIGINT, unix.SIGTERM, unix.SIGHUP, unix.SIGQUIT)
	defer signal.Stop(interrupts)
	hidden := *original
	hidden.Lflag &^= unix.ECHO | unix.ECHONL
	if unix.IoctlSetTermios(fd, secretSetTermios, &hidden) != nil {
		return nil, errors.New("cannot hide terminal input")
	}
	defer func() {
		if unix.IoctlSetTermios(fd, secretSetTermios, original) != nil {
			value = nil
			err = errors.New("cannot restore terminal input")
		}
		fmt.Fprintln(os.Stderr)
	}()
	if _, err := fmt.Fprintf(os.Stderr, "Paste the value for %s (hidden): ", name); err != nil {
		return nil, errors.New("cannot write secret prompt")
	}
	type result struct {
		line []byte
		err  error
	}
	read := make(chan result, 1)
	// An interrupted terminal read may stay blocked until the CLI exits; it
	// never owns terminal restoration or emits the value.
	go func() {
		line, err := bufio.NewReader(io.LimitReader(input, 16<<10+1)).ReadBytes('\n')
		read <- result{line, err}
	}()
	select {
	case <-interrupts:
		return nil, errors.New("secret entry interrupted")
	case got := <-read:
		if got.err != nil && got.err != io.EOF {
			return nil, errors.New("cannot read secret from terminal")
		}
		return readStdinSecret(bytes.NewReader(got.line))
	}
}
