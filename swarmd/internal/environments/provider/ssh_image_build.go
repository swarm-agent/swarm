package provider

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

var _ ImageBuilder = (*SSHDockerProvider)(nil)

// BuildNoEffectsError proves rejection before any remote build submission.
// It is produced by the provider, never by caller-provided receipt fields.
type BuildNoEffectsError struct{ Err error }

func (e *BuildNoEffectsError) Error() string { return e.Err.Error() }
func (e *BuildNoEffectsError) Unwrap() error { return e.Err }

// ConnectionBuildCleaner lets recovery use the authorized saved connection.
// A lost SSH build client cannot prove Docker's daemon-side build terminated.
type ConnectionBuildCleaner interface {
	CleanupConnectionBuild(context.Context, *environments.Connection, string) error
}

func sshBuildTag(conn *environments.Connection, id string) (string, error) {
	if conn == nil || conn.Kind != environments.ConnectionKindSSH || conn.Clone().Validate() != nil || !strings.HasPrefix(id, "op_") || ValidateOperationID(id) != nil {
		return "", errors.New("invalid SSH build connection or operation identity")
	}
	sum := sha256.Sum256([]byte(conn.AccountScopeID + "\x00" + conn.ID + "\x00" + id))
	return "swarm-managed:" + hex.EncodeToString(sum[:]), nil
}

// BuildImage streams a sanitized exact Git context directly to Docker. There is
// no remote filesystem staging, mutable source checkout, credential copy or sync.
// The operator's existing SSH authentication and Docker access are prerequisites.
func (p *SSHDockerProvider) BuildImage(ctx context.Context, req ImageBuildRequest) (_ *environments.ImageBuildResult, retErr error) {
	started := false
	defer func() {
		if retErr != nil && !started {
			retErr = &BuildNoEffectsError{Err: retErr}
		}
	}()
	tag, err := sshBuildTag(req.Connection, req.OperationID)
	if err != nil {
		return nil, err
	}
	if err := req.Definition.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, MaxOperationTimeout)
	defer cancel()
	if err := p.ValidateConnection(ctx, req.Connection); err != nil {
		return nil, err
	}
	// Bound the remote client too; timeout/transport loss remains unconfirmed,
	// never evidence of successful daemon-side cancellation.
	if _, err := p.runSSH(ctx, req.Connection, "timeout", "--version"); err != nil {
		return nil, errors.New("SSH build prerequisite unavailable: install GNU timeout on the authorized host")
	}
	root, err := os.MkdirTemp("", "swarm-ssh-context-")
	if err != nil {
		return nil, err
	}
	defer func() { retErr = errors.Join(retErr, os.RemoveAll(root)) }()
	digest, err := ExportCommittedBuild(ctx, p.runner, req.ProductRoot, req.RecipeRoot, root, req.Definition)
	if err != nil {
		return nil, err
	}
	// Refuse collisions: an inspect failure is not proof of absence.
	out, err := p.runSSH(ctx, req.Connection, "docker", "image", "ls", "--no-trunc", "--quiet", tag)
	if err != nil {
		return nil, errors.New("SSH build image collision preflight failed")
	}
	if strings.TrimSpace(string(out)) != "" {
		return nil, errors.New("SSH build operation tag already exists; no replacement permitted")
	}
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	transferCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		err := writeCommittedContext(transferCtx, writer, root)
		_ = writer.CloseWithError(err)
		done <- err
	}()
	var stdoutLog, stderrLog bytes.Buffer
	boundedOut := &boundedBuffer{buf: &stdoutLog, max: DefaultMaxOutputBytes / 2}
	boundedErr := &boundedBuffer{buf: &stderrLog, max: DefaultMaxOutputBytes / 2}
	remaining := MaxOperationTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline)
	}
	if remaining <= 0 {
		stop()
		_ = reader.Close()
		<-done
		return nil, ctx.Err()
	}
	started = true
	err = p.runSSHWithIO(ctx, req.Connection, reader, boundedOut, boundedErr,
		"timeout", "--signal=TERM", "--kill-after=10s", fmt.Sprintf("%.3fs", remaining.Seconds()), "docker", "build", "--network=none", "--tag", tag,
		"--label", "swarm.build_operation="+req.OperationID,
		"--label", "swarm.build_context="+digest,
		"--file", ".swarm-recipe/"+req.Definition.RecipeFile, "-")
	stop()
	_ = reader.Close()
	transferErr := <-done
	if err != nil || transferErr != nil || ctx.Err() != nil {
		// SSH may have lost contact while the daemon is still building. Retain
		// the exact operation tag for operator reconciliation; do not delete
		// a possibly-in-flight resource and invent a cleanup receipt.
		return nil, errors.Join(ErrOperationNotConfirmed, ctx.Err(), errors.New("SSH build/transfer failed; remote termination and operation-tag cleanup unconfirmed"))
	}
	out, err = p.runSSH(ctx, req.Connection, "docker", "image", "inspect", "--format", "{{json .}}", tag)
	if err != nil {
		return nil, errors.Join(ErrOperationCleanupFailed, errors.New("remote image provenance inspect unavailable; owned tag retained"))
	}
	var image struct {
		ID     string `json:"Id"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if json.Unmarshal(out, &image) != nil || !environments.ValidBuildImageID(image.ID) || image.Config.Labels["swarm.build_operation"] != req.OperationID || image.Config.Labels["swarm.build_context"] != digest {
		return nil, errors.Join(ErrOperationCleanupFailed, errors.New("remote image provenance mismatch; owned tag retained"))
	}
	return &environments.ImageBuildResult{OperationID: req.OperationID, ConnectionID: req.Connection.ID, ConnectionDigest: environments.ConnectionTransportDigest(req.Connection), DefinitionDigest: req.Definition.Digest(), ContextDigest: digest, ImageID: image.ID, Product: req.Definition.Product, Recipe: req.Definition.Recipe, RecipeFile: req.Definition.RecipeFile}, nil
}

// Recovery is deliberately honest: the daemon may still be producing a tag.
// Operators reconcile that exact tag after restoring access. No build replay.
func (p *SSHDockerProvider) CleanupBuild(context.Context, string) error {
	return errors.Join(ErrOperationNotConfirmed, errors.New("SSH build termination unconfirmed; reconcile operation on its authorized connection"))
}
func (p *SSHDockerProvider) CleanupConnectionBuild(_ context.Context, conn *environments.Connection, id string) error {
	tag, err := sshBuildTag(conn, id)
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: SSH build termination unconfirmed; reconcile owned image tag %s on the saved connection", ErrOperationNotConfirmed, tag)
}

func writeCommittedContext(ctx context.Context, output io.Writer, root string) error {
	tw := tar.NewWriter(output)
	total := int64(0)
	count := 0
	err := filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if file == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("unsafe staged context entry")
		}
		count++
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		if count > 60000 || total > maxBuildContext+(1<<20) {
			return errors.New("staged context exceeds bound")
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		h.ModTime = time.Unix(0, 0)
		h.Uid, h.Gid = 0, 0
		h.Uname, h.Gname = "", ""
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		_, err = io.CopyN(tw, f, info.Size())
		return errors.Join(err, f.Close())
	})
	return errors.Join(err, tw.Close())
}
