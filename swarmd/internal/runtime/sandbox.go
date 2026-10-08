package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm-refactor/swarmtui/pkg/storagecontract"
	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/config"
	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// homeCredentialDirs are paths under the daemon user's home that hold
// credentials for other tools; no sandbox mounts them and no file tool opens
// them or anything inside.
var homeCredentialDirs = []string{
	".ssh", ".gnupg", ".docker", ".aws", ".azure", ".kube", ".codex", ".claude",
	".config/gh", ".config/gcloud", ".config/swarm", ".netrc", ".git-credentials",
	".npmrc", ".pypirc",
}

// newSandboxManager builds the agent sandbox manager from daemon flags. Every
// daemon storage root is protected: no sandbox can mount it, a directory
// inside it, or a directory containing it.
func newSandboxManager(ctx context.Context, cfg config.Config, workspaces *pebblestore.WorkspaceStore) (*sandbox.Manager, error) {
	mode, err := sandbox.ParseMode(cfg.SandboxMode)
	if err != nil {
		return nil, err
	}
	storage, credentials, ancestors, err := protectedDaemonPaths(cfg)
	if err != nil {
		return nil, err
	}
	// File tools refuse the same paths, even with permissions bypassed.
	tool.SetProtectedPaths(storage, credentials)
	protected := append(append([]string(nil), storage...), credentials...)
	manager, err := sandbox.NewManager(ctx, sandbox.Config{
		Mode:               mode,
		Image:              cfg.SandboxImage,
		Network:            cfg.SandboxNetwork,
		Runtime:            cfg.SandboxRuntime,
		ProtectedRoots:     protected,
		ProtectedAncestors: ancestors,
		StateDir:           filepath.Join(cfg.DataDir, "sandbox"),
	}, nil)
	if err != nil {
		return nil, err
	}
	worktrees, err := appstorage.WorktreesRoot()
	if err != nil {
		return nil, fmt.Errorf("sandbox worktrees root: %w", err)
	}
	manager.SetLayout(sandbox.Layout{
		Projects:      workspaces.ListAllPaths,
		WorktreesRoot: worktrees,
		Bucket:        appstorage.WorktreeBucketName,
	})
	return manager, nil
}

// protectedDaemonPaths returns the daemon's storage roots (never inside,
// never containing), the daemon user's credential directories (never inside)
// and the directories a sandbox may mount below but never mount itself (the
// daemon user's home).
func protectedDaemonPaths(cfg config.Config) (storage, credentials, ancestors []string, err error) {
	roots, err := storagecontract.ResolveRoots(storagecontract.Options{})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve storage roots: %w", err)
	}
	storage = []string{roots.DataDir, roots.CacheDir, roots.RuntimeDir, roots.ConfigDir, roots.LogsDir, cfg.DataDir, filepath.Dir(cfg.DBPath), filepath.Dir(cfg.LockPath)}
	if path, err := startupconfig.ResolvePath(); err == nil {
		storage = append(storage, filepath.Dir(path))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		ancestors = append(ancestors, home)
		for _, dir := range homeCredentialDirs {
			credentials = append(credentials, filepath.Join(home, dir))
		}
	}
	return storage, credentials, ancestors, nil
}
