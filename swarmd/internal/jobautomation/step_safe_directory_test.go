package jobautomation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Purpose: a run step's checkout works when the folder belongs to another
// user than the lease's (the folder is the person's, the lease runs as its
// own uid). Git's safe.directory check refuses such a repository, and the
// lease's own GIT_CONFIG_COUNT safe.directory entries do not reach the
// upload-pack that git clone starts on a local path (git clears those
// variables for it), so the clone failed: "fatal: Could not read from remote
// repository ... and the repository exists" (exit 10). The prepare script
// trusts exactly the folder and its .git directory for every Git command
// that reads it, the clone's upload-pack included.
// Threat: production environment steps that could never check out a folder
// owned by the person (error_kind checkout, exit 10).
// Owners: prepareScript (safe.directory entries, clone --upload-pack) and
// EnvironmentExecutor.Run, over real Git. GIT_TEST_ASSUME_DIFFERENT_OWNER=1
// (Git's own test knob) makes every repository fail the ownership check, as
// the folder does in a lease; the fake lease trusts what a real lease would
// (the folder, as ExecLease does) plus the lease's own checkout, which in
// production belongs to the lease's user and passes the check by itself.
func TestEnvironmentCheckoutTrustsAFolderOwnedByAnotherUser(t *testing.T) {
	r := newJobRepo(t)
	trust := func(dirs ...string) []string {
		env := []string{"GIT_TEST_ASSUME_DIFFERENT_OWNER=1", "GIT_CONFIG_COUNT=" + strconv.Itoa(len(dirs))}
		for i, dir := range dirs {
			n := strconv.Itoa(i)
			env = append(env, "GIT_CONFIG_KEY_"+n+"=safe.directory", "GIT_CONFIG_VALUE_"+n+"="+dir)
		}
		return env
	}
	leaseEnv := func(home string) []string {
		checkout := filepath.Join(home, "swarm-step", "checkout")
		return trust(r.Folder, checkout, filepath.Join(checkout, ".git"))
	}

	// Without the script's trust, the clone fails the way production did,
	// even with the folder trusted through the lease's variables.
	home := t.TempDir()
	plain := exec.Command("git", "clone", "--quiet", "--no-checkout", "--shared", "--", r.Folder, filepath.Join(home, "swarm-step", "checkout"))
	plain.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1"}, leaseEnv(home)...)
	if out, err := plain.CombinedOutput(); err == nil || !strings.Contains(string(out), "dubious ownership") || !strings.Contains(string(out), "Could not read from remote repository") {
		t.Fatalf("fixture: a plain clone of the folder did not fail its ownership check: %v\n%s", err, out)
	}

	leases := newFakeLeases(t)
	leases.env = leaseEnv
	out, err := EnvironmentExecutor{Leases: leases}.Run(context.Background(), envRequest(r, "scripts/ok.sh", 60*time.Second))
	if err != nil || out.ExitCode != 0 {
		t.Fatalf("run: %v exit %d\n%s", err, out.ExitCode, out.Output)
	}
	got := string(out.Output)
	if !strings.Contains(got, "all good") {
		t.Fatalf("the step's script did not run:\n%s", got)
	}
	if strings.Contains(got, "shared clone set up by hand") || strings.Contains(got, "dubious ownership") {
		t.Fatalf("git clone itself did not trust the folder:\n%s", got)
	}
	if out.Repository == nil || !out.Repository.Clean || out.Repository.Problem != "" {
		t.Fatalf("repository check = %+v", out.Repository)
	}
}
