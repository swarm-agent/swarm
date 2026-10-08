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
)

// homeCredentialDirs are directories under the daemon user's home that hold
// credentials for other tools; no sandbox may mount them or anything inside.
var homeCredentialDirs = []string{".ssh", ".gnupg", ".docker", ".config", ".aws", ".kube", ".codex", ".claude", ".netrc"}

// newSandboxManager builds the agent sandbox manager from daemon flags. Every
// daemon storage root is protected: no sandbox can mount it, a directory
// inside it, or a directory containing it.
func newSandboxManager(ctx context.Context, cfg config.Config, workspaces *pebblestore.WorkspaceStore) (*sandbox.Manager, error) {
	mode, err := sandbox.ParseMode(cfg.SandboxMode)
	if err != nil {
		return nil, err
	}
	protected, ancestors, err := sandboxProtectedRoots(cfg)
	if err != nil {
		return nil, err
	}
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

func sandboxProtectedRoots(cfg config.Config) (protected, ancestors []string, err error) {
	roots, err := storagecontract.ResolveRoots(storagecontract.Options{})
	if err != nil {
		return nil, nil, fmt.Errorf("resolve storage roots for sandbox: %w", err)
	}
	protected = []string{roots.DataDir, roots.CacheDir, roots.RuntimeDir, roots.ConfigDir, roots.LogsDir, cfg.DataDir, filepath.Dir(cfg.DBPath), filepath.Dir(cfg.LockPath)}
	if path, err := startupconfig.ResolvePath(); err == nil {
		protected = append(protected, filepath.Dir(path))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		ancestors = append(ancestors, home)
		for _, dir := range homeCredentialDirs {
			protected = append(protected, filepath.Join(home, dir))
		}
	}
	return protected, ancestors, nil
}
