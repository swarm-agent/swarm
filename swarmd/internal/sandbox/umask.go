package sandbox

import (
	"fmt"
	"os/exec"
	"runtime"
)

// sandboxUmaskScript keeps new files writable by the shared daemon group and
// inaccessible to others. Shells and their descendants inherit this mask.
const sandboxUmaskScript = `umask 0007 && exec "$@"`

// withSandboxUmask preserves argv literally; command text is never interpolated
// into the wrapper. Worker and routed sandbox payloads start at this boundary.
func withSandboxUmask(command ...string) []string {
	return append([]string{"sh", "-c", sandboxUmaskScript, "swarm-exec"}, command...)
}

// gitUmaskScript is the mask of the daemon's Git on registered folders,
// routed by RouteGit or left on the host (hostGitUmask). Git
// writes into a person's folder object store, which leases read as other
// users: under 0007 its loose objects landed mode 0440 and its new fan-out
// directories 0770, so a step lease running as another user could not read
// them. 0002 lets Git give objects (created 0444) and directories the read
// bits core.sharedRepository and the folder's own modes allow, while group
// write (shared repositories) stays. Agents' own commands keep
// sandboxUmaskScript; the daemon repairs what they write
// (jobautomation.RepairObjectStore).
const gitUmaskScript = `umask 0002 && exec "$@"`

func withGitUmask(command ...string) []string {
	return append([]string{"sh", "-c", gitUmaskScript, "swarm-git"}, command...)
}

// hostGitUmask makes cmd, daemon Git on a registered folder that RouteGit
// runs on the host (not routed), run under gitUmaskScript too: sh execs the
// same git binary with the same arguments, Dir, Env and Stdin. A git that
// could not be resolved is left for the caller's Run to report (cmd.Err).
func hostGitUmask(cmd *exec.Cmd) error {
	if cmd.Err != nil || runtime.GOOS == "windows" {
		return nil
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		return fmt.Errorf("run git under umask 0002: %w", err)
	}
	cmd.Args = withGitUmask(append([]string{cmd.Path}, cmd.Args[1:]...)...)
	cmd.Path = sh
	return nil
}

// Image builds install root-owned files for users outside root's group. Unlike
// private worker files, these tools and profiles must remain readable/executable
// by other UIDs. Only BuildEnvironment's root setup/profile steps use this mask.
const imageBuildUmaskScript = `umask 022 && exec "$@"`

func withImageBuildUmask(command ...string) []string {
	return append([]string{"sh", "-c", imageBuildUmaskScript, "swarm-image-build"}, command...)
}
