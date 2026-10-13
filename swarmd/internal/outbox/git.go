package outbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/sandbox"
)

// Git runs through sandbox.Prepare like every other daemon Git call: on an
// agent-writable repository it runs in the project's sandbox, never as the
// daemon. Output is streamed over stdout, so nothing is written to a path
// the sandbox would have to see.

var mediaTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".avif": "image/avif", ".svg": "image/svg+xml",
	".mp4": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime", ".m4v": "video/mp4",
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".m4a": "audio/mp4", ".flac": "audio/flac",
}

// MediaType is the media type of an image, video or audio path, or "".
func MediaType(name string) string {
	return mediaTypes[strings.ToLower(path.Ext(name))]
}

// ContentType is the type to store a file with; never one a browser runs.
func ContentType(name string) string {
	if media := MediaType(name); media != "" && media != "image/svg+xml" {
		return media
	}
	if guess := mime.TypeByExtension(strings.ToLower(path.Ext(name))); strings.HasPrefix(guess, "text/plain") {
		return guess
	}
	return "application/octet-stream"
}

func (b *Builder) git(ctx context.Context, repo string, stdout io.Writer, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0")
	cmd.Stdout, cmd.Stderr = stdout, &limitedBuffer{buf: &stderr, max: 2048}
	sandbox.Prepare(ctx, cmd)
	if err := cmd.Run(); err != nil {
		if text := strings.TrimSpace(stderr.String()); text != "" {
			return fmt.Errorf("git %s: %w: %s", args[0], err, truncate(text, 300))
		}
		return fmt.Errorf("git %s: %w", args[0], err)
	}
	return nil
}

// GitOutput runs a Git command and returns its (bounded) output.
func (b *Builder) GitOutput(ctx context.Context, repo string, args ...string) (string, error) {
	var out bytes.Buffer
	if err := b.git(ctx, repo, &limitedBuffer{buf: &out, max: 1 << 20}, args...); err != nil {
		return "", err
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// Bundle writes the commits on branch after base as a Git bundle.
func (b *Builder) Bundle(ctx context.Context, repo, branch, base string) (Blob, error) {
	return b.capture(ctx, repo, "application/octet-stream", "bundle", "create", "-", "refs/heads/"+branch, "^"+base)
}

// File reads path at rev from the repository's objects (never the working
// folder). It refuses files over the outbox limit.
func (b *Builder) File(ctx context.Context, repo, rev, name string) (Blob, error) {
	object := rev + ":" + name
	size, err := b.GitOutput(ctx, repo, "cat-file", "-s", object)
	if err != nil {
		return Blob{}, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64)
	if err != nil {
		return Blob{}, fmt.Errorf("size of %s: %w", name, err)
	}
	if n > b.MaxFileBytes {
		return Blob{}, ErrTooLarge
	}
	return b.capture(ctx, repo, ContentType(name), "cat-file", "blob", object)
}

// ErrTooLarge is a file over SWARM_OUTBOX_MAX_FILE_MB.
var ErrTooLarge = errors.New("larger than the outbox file limit")

// capture streams a Git command's output into a temporary file, hashing it.
func (b *Builder) capture(ctx context.Context, repo, contentType string, args ...string) (Blob, error) {
	tmp, err := os.CreateTemp("", "swarm-outbox-*")
	if err != nil {
		return Blob{}, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmp.Name())
		}
	}()
	hash := sha256.New()
	counter := &capWriter{max: b.MaxFileBytes}
	if err := b.git(ctx, repo, io.MultiWriter(tmp, hash, counter), args...); err != nil {
		tmp.Close()
		if counter.over {
			return Blob{}, ErrTooLarge
		}
		return Blob{}, err
	}
	if err := tmp.Close(); err != nil {
		return Blob{}, err
	}
	keep = true
	return Blob{Path: tmp.Name(), SHA256: hex.EncodeToString(hash.Sum(nil)), Size: counter.n, Type: contentType}, nil
}

type capWriter struct {
	n, max int64
	over   bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	if w.max > 0 && w.n > w.max {
		w.over = true
		return 0, ErrTooLarge
	}
	return len(p), nil
}

type limitedBuffer struct {
	buf *bytes.Buffer
	max int
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}

// Bytes stores data a job produced in memory (a media job's files) as a blob.
func (b *Builder) Bytes(data []byte, contentType string) (Blob, error) {
	if b.MaxFileBytes > 0 && int64(len(data)) > b.MaxFileBytes {
		return Blob{}, ErrTooLarge
	}
	tmp, err := os.CreateTemp("", "swarm-outbox-*")
	if err != nil {
		return Blob{}, err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return Blob{}, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return Blob{}, err
	}
	sum := sha256.Sum256(data)
	return Blob{Path: tmp.Name(), SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), Type: contentType}, nil
}
