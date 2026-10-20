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
	"swarm/packages/swarmd/internal/identity"
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
func newSandboxManager(ctx context.Context, cfg config.Config, workspaces *pebblestore.WorkspaceStore, db *pebblestore.Store) (*sandbox.Manager, error) {
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
	manager, err := sandbox.NewManager(ctx, sandboxConfig(cfg, db, mode, cfg.SandboxBackend, "sandbox", storage, credentials, ancestors), nil)
	if err != nil {
		return nil, err
	}
	if err := setSandboxLayout(manager, workspaces); err != nil {
		return nil, err
	}
	return manager, nil
}

// newLeaseManager returns the Docker manager that runs agents' leases
// (sandbox/lease.go). It is its own manager, with its own state, whatever the
// sandbox setting is, so leases work with the sandbox off and survive the
// setting changing. It is nil, with the reason, when this machine has no
// working Docker.
func newLeaseManager(ctx context.Context, cfg config.Config, workspaces *pebblestore.WorkspaceStore, db *pebblestore.Store) (*sandbox.Manager, string) {
	storage, credentials, ancestors, err := protectedDaemonPaths(cfg)
	if err != nil {
		return nil, err.Error()
	}
	manager, err := sandbox.NewManager(ctx, sandboxConfig(cfg, db, sandbox.ModeAuto, sandbox.BackendDocker, "sandbox-leases", storage, credentials, ancestors), nil)
	if err != nil {
		return nil, err.Error()
	}
	if !manager.Active() {
		return nil, manager.Status().Reason
	}
	if err := setSandboxLayout(manager, workspaces); err != nil {
		return nil, err.Error()
	}
	return manager, ""
}

func sandboxConfig(cfg config.Config, db *pebblestore.Store, mode sandbox.Mode, backend, stateDir string, storage, credentials, ancestors []string) sandbox.Config {
	protected := append(append([]string(nil), storage...), credentials...)
	return sandbox.Config{
		Mode:    mode,
		Backend: backend,
		Image:   cfg.SandboxImage,
		Network: cfg.SandboxNetwork,
		Runtime: cfg.SandboxRuntime,
		Memory:  cfg.SandboxMemory,
		ProjectCacheLimit: func(ctx context.Context) (uint64, error) {
			return projectCacheLimit(ctx, db)
		},
		CPUs:               cfg.SandboxCPUs,
		CgroupParent:       cfg.SandboxCgroupParent,
		MaxWorkerRuns:      cfg.WorkerMaxConcurrentRuns,
		ProtectedRoots:     protected,
		ProtectedAncestors: ancestors,
		StateDir:           filepath.Join(cfg.DataDir, stateDir),
	}
}

func setSandboxLayout(manager *sandbox.Manager, workspaces *pebblestore.WorkspaceStore) error {
	worktrees, err := appstorage.WorktreesRoot()
	if err != nil {
		return fmt.Errorf("sandbox worktrees root: %w", err)
	}
	manager.SetLayout(sandbox.Layout{
		Projects:      workspaces.ListAllPaths,
		WorktreesRoot: worktrees,
		Bucket:        appstorage.WorktreeBucketName,
	})
	return nil
}

// leaseLimit is a folder's lease maximum: 0 for a folder without an
// environment or without max_leases (its agents' bash runs as usual), an
// error for one whose environment is set up for leases but not ready.
func leaseLimit(envs *pebblestore.SandboxEnvironmentStore) sandbox.LeaseLimit {
	return func(root string) (int, error) {
		env, found, err := envs.Get(root)
		if err != nil {
			return 0, err
		}
		if !found || env.MaxLeases <= 0 {
			return 0, nil
		}
		if env.ReadyImage == "" {
			return 0, fmt.Errorf("the environment of %s is %s, not ready: %s", root, env.Status, env.Error)
		}
		return env.MaxLeases, nil
	}
}

// leaseOwnerRunning reports whether an agent session still has a run.
func leaseOwnerRunning(sessions *pebblestore.SessionStore) sandbox.LeaseOwnerRunning {
	return func(owner string) (bool, error) {
		_, active, err := sessions.GetV3SessionActiveRunIntent(owner)
		return active, err
	}
}

func projectCacheLimit(ctx context.Context, db *pebblestore.Store) (uint64, error) {
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok {
		// Daemon automation has no principal; this soft cache cap is not an auth boundary.
		return sandbox.DefaultProjectCacheBytes, nil
	}
	limits, err := db.GetLimits(principal.AccountScopeID)
	return limits.ProjectCacheBytes, err
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
	if executable, err := os.Executable(); err == nil {
		storage = append(storage, executable)
	}
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

func reconcileWorkerSandboxes(ctx context.Context, m *sandbox.Manager, db *pebblestore.Store) error {
	ids, err := pebblestore.NewWorkerStore(db).ActiveWorkerSandboxSessions()
	if err != nil {
		return err
	}
	active := map[string]bool{}
	sessions := pebblestore.NewSessionStore(db)
	for _, id := range ids {
		snap, found, err := sessions.GetSession(id)
		if err != nil {
			return err
		}
		if found && snap.WorktreeRootPath != "" {
			active[sandbox.JobContainerName(snap.WorkspacePath, snap.WorktreeRootPath)] = true
			active[sandbox.ReviewContainerName(snap.WorkspacePath, snap.WorktreeRootPath)] = true
		}
	}
	return m.ReconcileContainers(ctx, func(name string) (bool, error) { return active[name], nil })
}
