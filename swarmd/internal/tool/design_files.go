package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/design"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// DesignFiles is design.Files for one workspace folder. Every path is
// workspace-relative; every component is opened from the workspace root
// without following symlinks (Linux openat with O_NOFOLLOW), so a design job
// never writes outside its folder or into protected storage, and never
// replaces a file it did not read. It runs in the daemon like the file tools;
// a design job has no managed execution binding, so nothing is routed to a
// managed checkout.
type DesignFiles struct {
	root        string
	resultsPath string
}

// NewDesignFiles binds design file operations to a canonical workspace root.
// resultsPath is the only file WriteResults may replace ("" refuses every
// results write, as in edit mode).
func NewDesignFiles(root, resultsPath string) (*DesignFiles, error) {
	root = strings.TrimSpace(root)
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("design files: workspace root must be an absolute clean path")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("design files: workspace root: %w", err)
	}
	if resolved != root {
		return nil, errors.New("design files: workspace root must not be reached through a symlink")
	}
	if err := refuseProtectedPath(root); err != nil {
		return nil, err
	}
	if err := refuseRootContainingStorage(root); err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("design files: workspace root: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("design files: workspace root is not a directory")
	}
	if resultsPath != "" && (!pebblestore.ValidDesignRelativePath(resultsPath) || path.Base(resultsPath) != design.ResultsFile) {
		return nil, errors.New("design files: invalid results path")
	}
	return &DesignFiles{root: root, resultsPath: resultsPath}, nil
}

var _ design.Files = (*DesignFiles)(nil)

// target validates rel and opens the workspace root for it.
func (f *DesignFiles) target(ctx context.Context, rel string) (*rootedWorkspacePath, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f == nil || f.root == "" {
		return nil, errors.New("design files are not configured")
	}
	if !pebblestore.ValidDesignRelativePath(rel) {
		return nil, fmt.Errorf("design files: %q is not a workspace-relative path inside the folder", rel)
	}
	abs := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := refuseProtectedPath(abs); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(f.root)
	if err != nil {
		return nil, fmt.Errorf("design files: open workspace root: %w", err)
	}
	return &rootedWorkspacePath{root: root, relative: filepath.FromSlash(rel), absolutePath: abs, mutable: true}, nil
}

// Read returns the file and its hex sha256.
func (f *DesignFiles) Read(ctx context.Context, rel string) ([]byte, string, error) {
	p, err := f.target(ctx, rel)
	if err != nil {
		return nil, "", err
	}
	defer p.Close()
	data, err := designReadNoFollow(p)
	if err != nil {
		return nil, "", err
	}
	return data, designDigest(data), nil
}

// CreateExclusive writes a new file, creating its parent folders inside the
// workspace, and refuses an existing file or symlink.
func (f *DesignFiles) CreateExclusive(ctx context.Context, rel string, data []byte) error {
	p, err := f.target(ctx, rel)
	if err != nil {
		return err
	}
	defer p.Close()
	return designCreateExclusive(p, data)
}

// WriteResults atomically replaces the job's results file and nothing else.
func (f *DesignFiles) WriteResults(ctx context.Context, rel string, data []byte) error {
	if f == nil || f.resultsPath == "" || rel != f.resultsPath {
		return fmt.Errorf("design files: %q is not this job's results file", rel)
	}
	p, err := f.target(ctx, rel)
	if err != nil {
		return err
	}
	defer p.Close()
	return designReplace(p, "", data)
}

// ReplaceIfDigest replaces a file only while it still has wantSHA256.
func (f *DesignFiles) ReplaceIfDigest(ctx context.Context, rel, wantSHA256 string, data []byte) error {
	if len(wantSHA256) != 64 {
		return errors.New("design files: a 64-character sha256 is required")
	}
	p, err := f.target(ctx, rel)
	if err != nil {
		return err
	}
	defer p.Close()
	return designReplace(p, strings.ToLower(wantSHA256), data)
}

// CheckFolder refuses a folder whose existing components are symlinks or
// not directories, before a job is accepted. The writes themselves stay the
// authority: each one walks the path again without following symlinks.
func (f *DesignFiles) CheckFolder(ctx context.Context, rel string) error {
	p, err := f.target(ctx, rel)
	if err != nil {
		return err
	}
	defer p.Close()
	prefix := ""
	for _, part := range strings.Split(p.relative, string(filepath.Separator)) {
		prefix = filepath.Join(prefix, part)
		info, err := p.root.Lstat(prefix)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("design files: %s: %w", filepath.ToSlash(prefix), err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("design files: %s is a symlink; refusing to follow it", filepath.ToSlash(prefix))
		}
		if !info.IsDir() {
			return fmt.Errorf("design files: %s is not a folder", filepath.ToSlash(prefix))
		}
	}
	return nil
}
