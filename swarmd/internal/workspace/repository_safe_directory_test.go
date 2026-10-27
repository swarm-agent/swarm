package workspace

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Purpose: the daemon's Git commands in a person's folder trust that folder
// by its exact path, so a folder owned by another uid (the person's, shared
// with Swarm's user) does not fail with "dubious ownership" when there is
// no sandbox to add safe.directory.
// Threat: repository setup and review failing in every shared home folder
// on a sandbox-off install, which strands project creation.
// Owners: the command line runRepositoryGitInput builds, checked through
// real Git: config --get safe.directory reports the folder.
func TestRepositoryGitTrustsItsFolder(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	if _, err := runRepositoryGit(dir, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	out, err := runRepositoryGit(dir, "config", "--get-all", "safe.directory")
	if err != nil || strings.TrimSpace(out) != dir {
		t.Fatalf("safe.directory not set for the folder: %v %q", err, out)
	}
}
