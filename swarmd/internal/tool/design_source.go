package tool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// DesignFileReference accepts paths, never model-supplied source bytes. Ranges
// are inclusive and must specify both ends; omission selects the complete file.
type DesignFileReference struct {
	Path      string `json:"path"`
	LineStart int    `json:"line_start,omitempty"`
	LineEnd   int    `json:"line_end,omitempty"`
}

type designSourceAuthorizerKey struct{}

// WithDesignSourceReadAuthorizer binds the run's canonical permission adapter.
// The adapter MUST authorize a separate secret-sensitive read of the indicated
// file for this principal/session (not reuse manage_design submit approval).
// All source reads are treated as sensitive, including innocently named files.
// Missing adapters fail closed. Install only from trusted run code, never args.
func WithDesignSourceReadAuthorizer(ctx context.Context, authorize func(context.Context, WorkspaceScope, string) error) context.Context {
	return context.WithValue(ctx, designSourceAuthorizerKey{}, authorize)
}

func designSourceDigest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hydrateDesignSources(ctx context.Context, scope WorkspaceScope, refs []DesignFileReference) ([]pebblestore.DesignContextSnapshot, error) {
	if len(refs) > 32 {
		return nil, pebblestore.ErrDesignInvalid
	}
	var snapshots []pebblestore.DesignContextSnapshot
	total := 0
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		snapshot, err := hydrateDesignSource(ctx, scope, ref)
		if err != nil {
			return nil, err
		}
		total += len(snapshot.Content)
		if total > pebblestore.MaxDesignContextBytes {
			return nil, errors.New("design source exceeds aggregate byte limit")
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func hydrateDesignSource(ctx context.Context, scope WorkspaceScope, ref DesignFileReference) (pebblestore.DesignContextSnapshot, error) {
	var zero pebblestore.DesignContextSnapshot
	if ref.Path == "" || len(ref.Path) > 4096 || strings.TrimSpace(ref.Path) != ref.Path || !utf8.ValidString(ref.Path) || ref.LineStart < 0 || ref.LineEnd < ref.LineStart || (ref.LineStart == 0 && ref.LineEnd != 0) || (ref.LineStart > 0 && ref.LineEnd-ref.LineStart >= 2000) {
		return zero, pebblestore.ErrDesignInvalid
	}
	for _, part := range strings.Split(filepath.ToSlash(ref.Path), "/") {
		if part == ".." {
			return zero, errors.New("design source traversal rejected")
		}
	}
	target, err := openRootedWorkspacePath(scope, ref.Path)
	if err != nil {
		return zero, err
	}
	defer target.Close()
	authorize, ok := ctx.Value(designSourceAuthorizerKey{}).(func(context.Context, WorkspaceScope, string) error)
	if !ok || authorize == nil {
		return zero, errors.New("design source read permission adapter unavailable")
	}
	if err := authorize(ctx, scope, target.absolutePath); err != nil {
		return zero, err
	}
	info, err := target.stat()
	if err != nil {
		return zero, err
	}
	if !info.Mode().IsRegular() || info.Size() > pebblestore.MaxDesignContextBytes {
		return zero, errors.New("design source must be a bounded regular file")
	}
	// Reuse the no-follow/nonblocking capture boundary to reject final symlink
	// swaps and avoid hanging on a FIFO swapped in after stat.
	file, err := openRecoverySourceFile(target.root, target.relative)
	if err != nil {
		return zero, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return zero, err
	}
	if !info.Mode().IsRegular() {
		return zero, errors.New("design source must be regular")
	}
	content, err := io.ReadAll(io.LimitReader(file, pebblestore.MaxDesignContextBytes+1))
	if err != nil {
		return zero, err
	}
	if len(content) > pebblestore.MaxDesignContextBytes || !utf8.Valid(content) || isLikelyBinary(content) {
		return zero, errors.New("design source is oversized or not UTF-8 text")
	}
	sourceHash := designSourceDigest(content)
	if ref.LineStart > 0 {
		lines := bytes.SplitAfter(content, []byte("\n"))
		if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
			lines = lines[:len(lines)-1]
		}
		if ref.LineEnd > len(lines) {
			return zero, errors.New("design source range exceeds file; no truncation allowed")
		}
		content = bytes.Join(lines[ref.LineStart-1:ref.LineEnd], nil)
	}
	return pebblestore.DesignContextSnapshot{Path: target.absolutePath, LineStart: ref.LineStart, LineEnd: ref.LineEnd, SourceSHA256: sourceHash, SHA256: designSourceDigest(content), Content: content}, nil
}
