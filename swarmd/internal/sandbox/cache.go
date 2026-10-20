package sandbox

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const projectCachePath = "/opt/swarm-cache"

// imageGoCachePath is the sandbox image's fixed, prebuilt Go build cache.
const imageGoCachePath = "/opt/go/cache"
const DefaultProjectCacheBytes uint64 = 10 << 30

// ProjectCacheVolume names a project-local cache, independent of job/review paths.
func ProjectCacheVolume(root string) string { return ContainerName(root) + "-build-cache" }

func (m *Manager) cacheLimit(ctx context.Context) (uint64, error) {
	if m.cfg.ProjectCacheLimit == nil {
		return DefaultProjectCacheBytes, nil
	}
	return m.cfg.ProjectCacheLimit(ctx)
}

// CacheUsage is a best-effort size observation, not a filesystem quota.
type CacheUsage struct {
	Volume     string `json:"volume"`
	Bytes      uint64 `json:"bytes"`
	LimitBytes uint64 `json:"limit_bytes"`
}

// cacheHelper mounts only the cache; never host storage, credentials or projects.
func (m *Manager) cacheHelper(ctx context.Context, root, script string, values ...string) ([]byte, error) {
	if m.Local() {
		return nil, fmt.Errorf("container project cache is unavailable with bubblewrap")
	}
	uid, gid := m.sandboxUser(root)
	args := []string{"run", "--network", "none", "--rm", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "64", "--memory", "512m"}
	m.mu.Lock()
	runtime := m.runtime
	m.mu.Unlock()
	if runtime != "" {
		args = append(args, "--runtime", runtime)
	}
	args = append(args, "--user", "0", "--cap-add", "CHOWN", "--cap-add", "FOWNER", "--cap-add", "DAC_OVERRIDE", "--mount", "type=volume,src="+ProjectCacheVolume(root)+",dst="+projectCachePath, "--entrypoint", "sh", m.cfg.Image)
	args = append(args, withSandboxUmask("sh", "-c", script, "swarm-cache", fmt.Sprintf("%d:%d", uid, gid))[1:]...)
	args = append(args, values...)
	return m.runner.Run(ctx, m.cfg.Engine, args...)
}

// Only initialization is locked. Go's build cache and pnpm's content-addressed
// store support concurrent consumers; no job holds this lock while executing.
const initializeCacheScript = `set -eu
cd ` + projectCachePath + `
exec 9>.init-lock
flock -x 9
if [ ! -e .ready ]; then
 if [ -z "$(find . -mindepth 1 -maxdepth 1 ! -name .init-lock -print -quit)" ]; then
  mkdir go-build go-mod pnpm
  if [ -d ` + imageGoCachePath + ` ]; then cp -a ` + imageGoCachePath + `/. go-build/; fi
  if [ -n "${GOMODCACHE:-}" ] && [ -d "$GOMODCACHE" ]; then cp -a "$GOMODCACHE/." go-mod/; fi
  if command -v pnpm >/dev/null 2>&1; then
   store=$(pnpm store path)
   if [ -d "$store" ]; then
    # store path includes the version directory; store-dir points to its parent.
    mkdir -p "pnpm/$(basename "$store")"
    cp -a "$store/." "pnpm/$(basename "$store")/"
   fi
  fi
 fi
 chown -R "$1" .
 chmod 0700 .
 touch .ready
fi`

func (m *Manager) initializeProjectCache(ctx context.Context, root string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := m.cacheHelper(ctx, root, initializeCacheScript); err != nil {
		return fmt.Errorf("initialize project cache: %w", err)
	}
	return nil
}

// ProjectCacheUsage reads without creating a volume. Errors are surfaced to callers.
func (m *Manager) ProjectCacheUsage(ctx context.Context, root string) (CacheUsage, error) {
	if m.Local() {
		return CacheUsage{}, fmt.Errorf("container project cache is unavailable with bubblewrap")
	}
	limit, err := m.cacheLimit(ctx)
	usage := CacheUsage{Volume: ProjectCacheVolume(root), LimitBytes: limit}
	if err != nil {
		return usage, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := m.runner.Run(ctx, m.cfg.Engine, "volume", "inspect", usage.Volume); err != nil {
		return usage, err
	}
	out, err := m.cacheHelper(ctx, root, `du -sk `+projectCachePath)
	if err != nil {
		return usage, err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return usage, fmt.Errorf("missing project cache size")
	}
	kb, err := strconv.ParseUint(fields[0], 10, 64)
	usage.Bytes = kb * 1024
	return usage, err
}

const trimCacheScript = `set -eu
size=$(du -sk ` + projectCachePath + ` | cut -f1)
if [ "$size" -gt "$1" ]; then
 if command -v go >/dev/null 2>&1; then go clean -cache; fi
fi`

// Trim at container admission, never delete module or pnpm content being used by
// running jobs. This is a soft cap: active writers and dependency stores can
// exceed it. Go clean uses the Go cache's own removal semantics.
func (m *Manager) trimProjectCache(ctx context.Context, name string) error {
	limit, err := m.cacheLimit(ctx)
	if err != nil {
		return fmt.Errorf("read project cache limit: %w", err)
	}
	_, err = m.runner.Run(ctx, m.cfg.Engine, append([]string{"exec", name}, withSandboxUmask("sh", "-c", trimCacheScript, "swarm-cache-trim", strconv.FormatUint(limit/1024, 10))...)...)
	return err
}
