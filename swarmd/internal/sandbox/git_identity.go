package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"net/mail"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"
)

const sandboxUmaskScript = `umask 0007 && exec "$@"`

// initializeHome repairs Docker's root-owned volume root without giving agent
// processes capabilities. Only the dedicated home volume reaches the helper;
// no project, daemon storage, network, or image entrypoint is exposed to it.
func (m *Manager) initializeHome(ctx context.Context, name, root string, image EnvironmentImage) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	uid, gid := m.sandboxUser(root)
	owner := fmt.Sprintf("%d:%d", uid, gid)
	out, err := m.runner.Run(ctx, m.cfg.Engine, "exec", "-u", "0", name, "stat", "-c", "%u:%g %a", sandboxHome)
	if err == nil && strings.TrimSpace(string(out)) == owner+" 700" {
		return
	}
	args := []string{"run", "--rm", "--user", "0", "--network", "none", "--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "FOWNER", "--security-opt", "no-new-privileges", "--read-only"}
	m.mu.Lock()
	runtime := m.runtime
	m.mu.Unlock()
	if runtime != "" {
		args = append(args, "--runtime", runtime)
	}
	args = append(args, "--mount", "type=volume,src="+name+"-home,dst="+sandboxHome, "--entrypoint", "sh", image.Image,
		"-c", `chown "$1" "$2" && chmod 0700 "$2"`, "swarm-home", owner, sandboxHome)
	if _, err := m.runner.Run(ctx, m.cfg.Engine, args...); err != nil {
		slog.Warn("initialize sandbox home failed", "error", err)
	}
}

// configureGitIdentity runs only at creation. Fill missing global components
// in the sandbox home; Git's repository-local configuration still wins.
func (m *Manager) configureGitIdentity(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	author, email := "Swarm", "swarm@localhost"
	if raw := os.Getenv("SWARM_GIT_AUTHOR"); raw != "" {
		addr, err := mail.ParseAddress(raw)
		invalid := strings.ContainsFunc(raw, unicode.IsControl) || !strings.Contains(raw, "<") || !strings.HasSuffix(strings.TrimSpace(raw), ">")
		if err != nil || invalid || addr.Name == "" || !strings.Contains(addr.Address, "@") || strings.ContainsAny(addr.Name, "<>") || strings.ContainsFunc(addr.Address, unicode.IsSpace) {
			slog.Warn("invalid SWARM_GIT_AUTHOR; using default sandbox Git identity")
		} else {
			author, email = addr.Name, addr.Address
		}
	}
	values := []string{author, email}
	for i, key := range []string{"user.name", "user.email"} {
		// This reads only daemon-owned global config, never a project config.
		value, err := exec.CommandContext(ctx, "git", "config", "--global", "--get", key).Output()
		if v := strings.TrimSpace(string(value)); err == nil && v != "" {
			values[i] = v
		}
	}
	// One engine call checks and fills both components, with explicit HOME and
	// positional arguments (identity text is never interpreted as shell code).
	const script = `umask 0007
for key in user.name user.email; do
 value=$(git config --global --get "$key")
 status=$?
 if [ "$status" -ne 0 ] && [ "$status" -ne 1 ]; then exit "$status"; fi
 if [ -z "$value" ]; then git config --global "$key" "$1" || exit "$?"; fi
 shift
done`
	if _, err := m.runner.Run(ctx, m.cfg.Engine, "exec", "-e", "HOME="+sandboxHome, "-w", sandboxHome, name, "sh", "-c", script, "swarm-identity", values[0], values[1]); err != nil {
		return fmt.Errorf("configure sandbox Git identity: %w", err)
	}
	return nil
}
