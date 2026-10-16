package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"swarm/packages/swarmd/internal/runner"
)

func runnerMain(args []string) error {
	flags := flag.NewFlagSet("runner", flag.ContinueOnError)
	hub := flags.String("hub", "", "hub URL")
	credential := flags.String("credential-file", "", "invoking-user-owned 0600 bearer credential file")
	work := flags.String("work-dir", "", "disposable landing directory (defaults to a temporary directory)")
	state := flags.String("state-file", "", "runner state file (service uses /run/swarm-runner/state)")
	name := flags.String("name", "", "runner display name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *hub == "" || *credential == "" {
		return errors.New("usage: swarmd runner --hub URL --credential-file PATH [--work-dir DIR] [--name NAME]")
	}
	token, err := readRunnerCredential(*credential)
	if err != nil {
		return err
	}
	if *work == "" {
		dir, err := os.MkdirTemp("", "swarm-runner-")
		if err != nil {
			return err
		}
		*work = dir
		defer os.RemoveAll(dir)
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	r := runner.Runner{StateFile: *state, Hub: *hub, Credential: token, WorkDir: *work, Name: *name, Logger: logger, Engine: &runner.SandboxEngine{Log: logger.Printf}}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	err = r.Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func readRunnerCredential(path string) (string, error) {
	// Open without following a symlink and inspect the same descriptor we read.
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || !ok || owner.Uid != uint32(os.Geteuid()) {
		return "", errors.New("runner credential must be a regular file owned by the invoking user with mode 0600")
	}
	b, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if token == "" || len(b) > 16384 || strings.ContainsAny(token, "\r\n\x00") {
		return "", fmt.Errorf("invalid runner credential file")
	}
	return token, nil
}
