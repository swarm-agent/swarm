package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func scopeContainerName(scope Scope) string {
	if scope.ReviewCheckout != "" {
		return ContainerName(scope.Root) + "-review-" + filepath.Base(ContainerName(scope.ReviewCheckout))
	}
	return ContainerName(scope.Root)
}

// RegisterReviewCheckout is daemon-owned, never exposed as an agent tool.
// It must be called before any reviewer command, including after restart.
func (m *Manager) RegisterReviewCheckout(checkout string) error {
	if m == nil || !m.Active() {
		return ErrSandboxRequired
	}
	scope, ok, err := m.ScopeFor(checkout)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("review checkout is outside a registered project")
	}
	checkout = resolveExisting(checkout)
	if checkout == scope.Root {
		return fmt.Errorf("review checkout must be disposable, not the source repository")
	}
	if _, err := reviewGitMounts(checkout, scope.Root); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reviews == nil {
		m.reviews = map[string]string{}
	}
	m.reviews[checkout] = scope.Root
	return nil
}

func (m *Manager) reviewScope(scope Scope, path string) Scope {
	m.mu.Lock()
	defer m.mu.Unlock()
	for checkout, root := range m.reviews {
		if within(checkout, resolveExisting(path)) {
			scope.Root = root
			scope.ReviewCheckout = checkout
			return scope
		}
	}
	return scope
}

// reviewGitMounts protects both the checkout's .git pointer and the whole
// common Git directory (including worktree indices/HEADs), not just refs.
func reviewGitMounts(checkout, root string) ([]FileMount, error) {
	common := filepath.Join(root, ".git")
	info, err := os.Lstat(common)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("review requires a project with a directory .git")
	}
	pointer := filepath.Join(checkout, ".git")
	data, err := os.ReadFile(pointer)
	if err != nil {
		return nil, fmt.Errorf("review checkout git pointer: %w", err)
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(string(data), "gitdir:"))
	if !strings.HasPrefix(string(data), "gitdir:") {
		return nil, fmt.Errorf("invalid review checkout git pointer")
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(checkout, gitdir)
	}
	if !within(resolveExisting(common), resolveExisting(gitdir)) {
		return nil, fmt.Errorf("review checkout uses a foreign git directory")
	}
	return []FileMount{{HostPath: common, Dest: common}, {HostPath: pointer, Dest: pointer}}, nil
}

func (m *Manager) scopeRunArgs(name, root, spec string, mounts []string, secret SecretSetup, image EnvironmentImage, checkout string) []string {
	args := m.runArgsImage(name, root, spec, mounts, secret, image)
	if checkout == "" {
		return args
	}
	// Turn every shared project/bucket mount read-only. The narrow writable
	// checkout overlay is appended before the image; Git overlays follow it.
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "--mount" {
			continue
		}
		for _, mount := range mounts {
			if args[i+1] == "type=bind,src="+mount+",dst="+mount {
				args[i+1] += ",readonly"
			}
		}
	}
	guards, _ := reviewGitMounts(checkout, root) // validated by acquire
	extra := []string{"--env", "GIT_OPTIONAL_LOCKS=0", "--mount", "type=bind,src=" + checkout + ",dst=" + checkout}
	for _, guard := range guards {
		extra = append(extra, "--mount", "type=bind,src="+guard.HostPath+",dst="+guard.Dest+",readonly")
	}
	// Insert before the image/entrypoint tail, never between --entrypoint
	// and its value for folder-specific environment images.
	at := len(args) - 3
	if image.Image != m.cfg.Image {
		at = len(args) - 4
	}
	// Reviewer container startup also inherits the same group-write umask as
	// supervised commands and routed Git. Explicit entrypoint avoids inheriting
	// arbitrary folder image startup behavior.
	tail := append([]string{"--entrypoint", "sh", image.Image}, withSandboxUmask("sleep", "infinity")[1:]...)
	return append(append(append([]string{}, args[:at]...), extra...), tail...)
}
