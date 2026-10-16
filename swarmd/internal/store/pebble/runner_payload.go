package pebblestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"swarm/packages/swarmd/internal/sandbox"
)

const RunnerArchiveLimit int64 = 512 << 20

type runnerArchiveWriter struct {
	dst       io.Writer
	remaining int64
	size      int64
	exceeded  bool
}

func (w *runnerArchiveWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		w.exceeded = true
		return 0, errors.New("runner archive exceeds 512 MiB")
	}
	n, err := w.dst.Write(p)
	w.remaining -= int64(n)
	w.size += int64(n)
	return n, err
}
func (s *Store) runnerPayloadPath(account, id string) string {
	return filepath.Join(s.path, "runner-payloads", runnerHash(account), runnerHash(id), "source.tar.gz")
}
func (s *Store) buildRunnerPayload(account string, l *RunnerLanding) error {
	if l.SourceClonePath == "" {
		return errors.New("runner source_clone_path required")
	}
	// Accept only immutable object IDs, never Git options or revision expressions.
	raw, err := hex.DecodeString(l.HeadCommit)
	if err != nil || (len(raw) != 20 && len(raw) != 32) {
		return errors.New("runner head_commit must be a full object ID")
	}
	path := s.runnerPayloadPath(account, l.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	hash := sha256.New()
	writer := &runnerArchiveWriter{dst: io.MultiWriter(f, hash), remaining: RunnerArchiveLimit}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := sandbox.Command(ctx, "-C", l.SourceClonePath, "archive", "--format=tar.gz", l.HeadCommit)
	cmd.Stdout = writer
	if err := cmd.Run(); err != nil {
		if writer.exceeded {
			return errors.New("runner archive exceeds 512 MiB")
		}
		return fmt.Errorf("create runner archive: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	l.PayloadFormat = "archive-v1"
	l.ArchiveSHA256 = hex.EncodeToString(hash.Sum(nil))
	l.ArchiveBytes = writer.size
	ok = true
	return nil
}

// OpenRunnerPayload verifies durable ownership before opening daemon-owned data.
func (s *Store) OpenRunnerPayload(account, runner, id string, now time.Time) (*os.File, error) {
	runnerStateMu.Lock()
	defer runnerStateMu.Unlock()
	var st runnerState
	if _, err := s.GetJSON("runners:v1:"+runnerHash(account), &st); err != nil {
		return nil, err
	}
	for _, l := range st.Landings {
		if l.ID == id && l.State == "claimed" && l.Runner == runner && l.LeaseUntil > now.UnixMilli() {
			return os.Open(s.runnerPayloadPath(account, id))
		}
	}
	return nil, errors.New("landing payload not owned or lease expired")
}
