package jobautomation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// gitUnderUmask runs git in dir as an agent lease does: under umask mask
// (the sandbox's 0007), with the folder's own core.sharedRepository.
func gitUnderUmask(t *testing.T, mask, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("sh", append([]string{"-c", "umask " + mask + " && exec \"$@\"", "swarm-test", "git", "-c", "user.name=Swarm Test", "-c", "user.email=test@example.invalid"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func statOf(t *testing.T, path string) (os.FileMode, int) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode(), int(info.Sys().(*syscall.Stat_t).Gid)
}

// Purpose: an object an agent wrote into a folder's store shared with
// core.sharedRepository=group under the sandbox's restrictive umask (mode
// 0440, a fan-out directory 0770 in the writer's primary group, no setgid)
// is made readable by whoever the folder's objects directory lets read it
// (here 2775: group and others) and gets the folder's group back, and the
// repair is idempotent; a symbolic link in the store is never followed.
// Threat: the production failure: 177 loose objects mode 0440 (some in
// swarm:swarm fan-out directories) that a step lease running as another
// user could not open, fixed only by a manual chmod -R a+rX.
// Owners: RepairObjectStore over a real Git store written by real Git under
// umask 0007; the narrowest layer that sees the modes Git produced.
func TestObjectStoreRepairMakesAgentObjectsReadable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	folder := t.TempDir()
	runGit(t, folder, "init", "-q", "-b", "dev")
	runGit(t, folder, "config", "core.sharedRepository", "group")
	objects := filepath.Join(folder, ".git", "objects")
	if err := os.Chmod(objects, 0o775); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, folder, "agent.txt", "written by an agent\n", 0o644)
	id := gitUnderUmask(t, "0007", folder, "hash-object", "-w", "agent.txt")
	object := filepath.Join(objects, id[:2], id[2:])
	fanOut := filepath.Dir(object)

	// The folder's group: a supplementary group of the test user when it
	// has one (as the person's group is for Swarm's user), so a fan-out
	// directory left in the writer's primary group is detectable.
	folderGid := os.Getgid()
	if groups, _ := os.Getgroups(); len(groups) > 0 {
		for _, g := range groups {
			if g != os.Getgid() {
				folderGid = g
				break
			}
		}
	}
	if err := os.Chown(objects, -1, folderGid); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(objects, 0o775|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	mode, _ := statOf(t, object)
	if mode.Perm()&0o004 != 0 {
		t.Fatalf("fixture: the agent's object is already world-readable (%v), so it proves nothing", mode.Perm())
	}
	// A link in the store to a private file outside it.
	private := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(private, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, filepath.Join(fanOut, "link")); err != nil {
		t.Fatal(err)
	}

	result, ok := RepairObjectStore(folder)
	if !ok || result.Error != "" || result.Unrepaired != 0 || result.Repaired < 2 {
		t.Fatalf("repair = %+v ok %v", result, ok)
	}
	mode, gid := statOf(t, object)
	if mode.Perm()&0o444 != 0o444 || gid != folderGid {
		t.Fatalf("object after repair: mode %v gid %d, want readable by all, group %d", mode.Perm(), gid, folderGid)
	}
	mode, gid = statOf(t, fanOut)
	if mode.Perm()&0o055 != 0o055 || gid != folderGid {
		t.Fatalf("fan-out directory after repair: mode %v gid %d, want g+rx o+rx, group %d", mode, gid, folderGid)
	}
	if mode&os.ModeSetgid == 0 && result.SetgidRefused == 0 {
		t.Fatalf("fan-out directory not setgid and no refusal recorded: %+v", result)
	}
	if info, _ := os.Stat(private); info.Mode().Perm() != 0o600 {
		t.Fatalf("the repair followed a link: the private file is now %v", info.Mode().Perm())
	}
	// A reader that is neither owner nor in the group can now open it.
	if data, err := os.ReadFile(object); err != nil || len(data) == 0 {
		t.Fatalf("read object: %v", err)
	}
	again, _ := RepairObjectStore(folder)
	if again.Repaired != 0 || again.Unrepaired != 0 {
		t.Fatalf("second pass changed something: %+v", again)
	}

	// A new fan-out directory Git creates in the setgid objects directory
	// keeps the folder's group (the kernel passes setgid on).
	writeRepoFile(t, folder, "later.txt", "a later object\n", 0o644)
	later := gitUnderUmask(t, "0002", folder, "hash-object", "-w", "later.txt")
	if later[:2] != id[:2] {
		if _, gid := statOf(t, filepath.Join(objects, later[:2])); gid != folderGid {
			t.Fatalf("new fan-out directory group %d, want the folder's %d", gid, folderGid)
		}
	}
	if mode, _ := statOf(t, filepath.Join(objects, later[:2], later[2:])); mode.Perm()&0o444 != 0o444 {
		t.Fatalf("an object written under umask 0002 is %v, want readable by all", mode.Perm())
	}
}

// Purpose: a writer of the store (the folder's owner, its group, an agent
// committing as the daemon) that swaps a fan-out directory, or one object,
// for a symbolic link after the repair listed it and before it changes it
// cannot make the daemon change the link's target: the walk opens every
// entry with O_NOFOLLOW relative to its parent's descriptor and changes
// only the opened inode, so the private directory and file the link points
// at keep their modes and the walk goes on.
// Threat: the reviewed race: os.ReadDir and os.Chmod followed a link
// swapped in after the Lstat check, so the daemon added read bits (and the
// folder's group) to its own private files, or any file when it is root.
// Owners: RepairObjectStore, repairPolicy.repairDir, repairFile and fix; the
// swap runs at objectStoreRepairSwap, the exact window between listing and
// opening, which makes the race deterministic.
func TestObjectStoreRepairNeverFollowsALinkSwappedInMidPass(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	t.Cleanup(func() { objectStoreRepairSwap = nil })
	setup := func(t *testing.T) (folder, object, privateDir, privateFile string) {
		t.Helper()
		folder = t.TempDir()
		runGit(t, folder, "init", "-q", "-b", "dev")
		if err := os.Chmod(filepath.Join(folder, ".git", "objects"), 0o775); err != nil {
			t.Fatal(err)
		}
		writeRepoFile(t, folder, "agent.txt", "written by an agent\n", 0o644)
		id := gitUnderUmask(t, "0077", folder, "hash-object", "-w", "agent.txt")
		object = filepath.Join(folder, ".git", "objects", id[:2], id[2:])
		if mode, _ := statOf(t, object); mode.Perm()&0o044 != 0 {
			t.Fatalf("fixture: the object is already readable (%v), so it proves nothing", mode.Perm())
		}
		privateDir = filepath.Join(t.TempDir(), "daemon-state")
		if err := os.Mkdir(privateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		privateFile = filepath.Join(privateDir, "credentials")
		if err := os.WriteFile(privateFile, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		return folder, object, privateDir, privateFile
	}
	unchanged := func(t *testing.T, privateDir, privateFile string) {
		t.Helper()
		if mode, _ := statOf(t, privateDir); mode.Perm() != 0o700 {
			t.Fatalf("the repair followed a swapped-in link: the private directory is now %v", mode.Perm())
		}
		if mode, _ := statOf(t, privateFile); mode.Perm() != 0o600 {
			t.Fatalf("the repair followed a swapped-in link: the private file is now %v", mode.Perm())
		}
	}

	t.Run("fan-out directory swapped for a link", func(t *testing.T) {
		folder, object, privateDir, privateFile := setup(t)
		fanOut := filepath.Dir(object)
		swapped := false
		objectStoreRepairSwap = func(path string) {
			if path != fanOut || swapped {
				return
			}
			swapped = true
			if err := os.Rename(fanOut, fanOut+".moved"); err != nil {
				t.Error(err)
			}
			if err := os.Symlink(privateDir, fanOut); err != nil {
				t.Error(err)
			}
		}
		result, ok := RepairObjectStore(folder)
		objectStoreRepairSwap = nil
		if !swapped || !ok || result.Error != "" {
			t.Fatalf("repair = %+v ok %v swapped %v", result, ok, swapped)
		}
		unchanged(t, privateDir, privateFile)
		if info, err := os.Lstat(fanOut); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the link in the store was replaced or removed: %v %v", info, err)
		}
	})

	t.Run("object swapped for a link", func(t *testing.T) {
		folder, object, privateDir, privateFile := setup(t)
		swapped := false
		objectStoreRepairSwap = func(path string) {
			if path != object || swapped {
				return
			}
			swapped = true
			if err := os.Rename(object, object+".moved"); err != nil {
				t.Error(err)
			}
			if err := os.Symlink(privateFile, object); err != nil {
				t.Error(err)
			}
		}
		result, ok := RepairObjectStore(folder)
		objectStoreRepairSwap = nil
		if !swapped || !ok || result.Error != "" {
			t.Fatalf("repair = %+v ok %v swapped %v", result, ok, swapped)
		}
		unchanged(t, privateDir, privateFile)
		// The walk went on: the fan-out directory itself was repaired.
		if mode, _ := statOf(t, filepath.Dir(object)); mode.Perm()&0o055 != 0o055 {
			t.Fatalf("fan-out directory %v after repair, want g+rx o+rx", mode.Perm())
		}
	})
}

// Purpose: the repair is recorded in the folder's repository health: one
// that made entries readable is kept (get_environment shows it), one that
// left entries it could not repair is a problem the box summary lists, and
// a pass that found everything readable clears it. A folder without its own
// .git directory is skipped, not reported.
// Threat: a repair that silently fails, or a stale warning after the store
// is fixed.
// Owners: Runner.RepairFolderObjects, Store.RecordObjectStoreRepair,
// ObjectStoreRepairOf, ObjectStoreRepairProblems, over a real store.
func TestObjectStoreRepairRecordedInRepositoryHealth(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	_, store := openTestStore(t)
	runner := NewRunner(Options{Store: store})
	folder := t.TempDir()
	runGit(t, folder, "init", "-q", "-b", "dev")
	if err := os.Chmod(filepath.Join(folder, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, folder, "a.txt", "a\n", 0o644)
	id := gitUnderUmask(t, "0077", folder, "hash-object", "-w", "a.txt")
	object := filepath.Join(folder, ".git", "objects", id[:2], id[2:])
	notRepo := t.TempDir()

	runner.RepairFolderObjects([]string{folder, notRepo, folder})
	got, found, err := store.ObjectStoreRepairOf(folder)
	if err != nil || !found || got.Repaired == 0 || got.Problem() || got.At == 0 {
		t.Fatalf("recorded repair = %+v found %v err %v", got, found, err)
	}
	if mode, _ := statOf(t, object); mode.Perm()&0o044 != 0o044 {
		t.Fatalf("object mode %v after repair", mode.Perm())
	}
	if _, found, _ := store.ObjectStoreRepairOf(notRepo); found {
		t.Fatal("a folder without a repository got a repair record")
	}
	if problems, err := store.ObjectStoreRepairProblems(); err != nil || len(problems) != 0 {
		t.Fatalf("problems = %+v %v", problems, err)
	}

	// An entry it could not repair is a problem with a sample.
	failed := ObjectStoreRepair{Folder: folder, Checked: 3, Unrepaired: 1, Sample: object + ": mode -r--r----- owner 1:2, not Swarm's to change"}
	if err := store.RecordObjectStoreRepair(failed); err != nil {
		t.Fatal(err)
	}
	problems, err := store.ObjectStoreRepairProblems()
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0].Message(), "1 object store entries run steps cannot read") || !strings.Contains(problems[0].Message(), folder) {
		t.Fatalf("problems = %+v %v", problems, err)
	}

	// Everything readable: cleared.
	runner.RepairFolderObjects([]string{folder})
	if _, found, _ := store.ObjectStoreRepairOf(folder); found {
		t.Fatal("a clean pass did not clear the record")
	}
}

// Purpose: a step's checkout failure keeps git's whole error, never a
// fragment: every line (a long error keeps its first and last lines with a
// marker), the failing git command named, in the step reason and in the
// repository health record.
// Threat: the production reason "could not make the step's checkout in the
// environment (exit 10): and the repository exists." that lost git's
// actual message.
// Owners: prepareFailure, prepareGitError, checkoutFailure, parseCheckout,
// and EnvironmentExecutor.Run over the real prepare script with a folder
// that is not a repository (both git clone and the clone by hand fail).
func TestEnvironmentCheckoutKeepsGitsFullError(t *testing.T) {
	output := strings.Join([]string{
		"fatal: unable to read 0123456789012345678901234567890123456789",
		"fatal: Could not read from remote repository.",
		"",
		"Please make sure you have the correct access rights",
		"and the repository exists.",
		"swarm-step-git git clone --no-checkout --shared /srv/folder",
	}, "\n")
	check := parseCheckout(output)
	err := prepareFailure(prepareCloneFailed, "scripts/check.sh", strings.Repeat("ab", 20), output, check)
	var start *StartError
	if !errors.As(err, &start) || start.Kind != ErrorKindCheckout {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"(exit 10)", "git clone --no-checkout --shared /srv/folder failed", "fatal: unable to read 0123456789012345678901234567890123456789", "Could not read from remote repository.", "Please make sure you have the correct access rights", "and the repository exists."} {
		if !strings.Contains(start.Reason, want) {
			t.Fatalf("reason lacks %q:\n%s", want, start.Reason)
		}
	}
	repo := checkoutFailure(prepareCloneFailed, output, check)
	if repo == nil || repo.Problem != RepositoryCheckoutFailed || repo.Command != "git clone --no-checkout --shared /srv/folder" || !strings.Contains(repo.Detail, "fatal: unable to read") || !strings.Contains(repo.Detail, "and the repository exists.") {
		t.Fatalf("repository check = %+v", repo)
	}
	if checkoutFailure(prepareScriptMissing, output, check) != nil || checkoutFailure(prepareHeadMissing, output, check) != nil {
		t.Fatal("a missing script or head is recorded as a repository problem")
	}

	var long []string
	for i := 0; i < 40; i++ {
		long = append(long, "git error line "+strconv.Itoa(i))
	}
	text := prepareGitError(strings.Join(long, "\n"))
	if !strings.HasPrefix(text, long[0]) || !strings.HasSuffix(text, long[39]) || !strings.Contains(text, "[... 28 lines of git's error left out ...]") {
		t.Fatalf("long error = %q", text)
	}

	// The real script: the folder is not a repository.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	r := jobRepo{Folder: t.TempDir(), Head: strings.Repeat("ab", 20)}
	out, err := EnvironmentExecutor{Leases: newFakeLeases(t)}.Run(context.Background(), envRequest(r, "scripts/ok.sh", 60*time.Second))
	if !errors.As(err, &start) || start.Kind != ErrorKindCheckout {
		t.Fatalf("err = %v\n%s", err, out.Output)
	}
	for _, want := range []string{"git clone failed: fatal: repository", "does not exist", "git init with " + r.Folder + "'s objects as alternates and its branches as origin/* failed", "not a git repository"} {
		if !strings.Contains(start.Reason, want) {
			t.Fatalf("reason lacks %q:\n%s", want, start.Reason)
		}
	}
	if out.Repository == nil || out.Repository.Problem != RepositoryCheckoutFailed || !strings.HasPrefix(out.Repository.Command, "git init with ") || !strings.Contains(out.Repository.Detail, "not a git repository") {
		t.Fatalf("repository check = %+v", out.Repository)
	}
	_, store := openTestStore(t)
	if err := store.RecordRepositoryCheck(r.Folder, out.Repository); err != nil {
		t.Fatal(err)
	}
	health, found, err := store.RepositoryHealthOf(r.Folder)
	if err != nil || !found || health.Command != out.Repository.Command || !strings.Contains(health.Message(), "could not be checked out by a run step: git init with") || !strings.Contains(health.Message(), "not a git repository") {
		t.Fatalf("health = %+v (%q) found %v err %v", health, health.Message(), found, err)
	}
}

// Purpose: a branch tip object the step's lease may not read no longer
// fails the whole checkout before any fallback: git clone fails (it reads
// every branch tip), the script makes the same shared clone by hand, finds
// the unreadable object and makes the checkout from the folder's origin.
// Threat: the production checkout error (exit 10) that the origin fallback
// never got to rescue, because the clone failed first.
// Owners: prepareScript (clone by hand), parseCheckout (by-hand note),
// EnvironmentExecutor.Run, over real Git with a mode 0000 head commit.
func TestEnvironmentCheckoutClonesByHandWhenABranchTipIsUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode 0000 file: an unreadable object needs a non-root test user")
	}
	r, _ := originRepo(t, true)
	// The job's head integrated: the folder's dev (its HEAD branch) now
	// points at a commit Swarm wrote, which the lease may not read.
	runGit(t, r.Folder, "update-ref", "refs/heads/dev", r.Head)
	path := looseObject(t, r, r.Head)
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o444) })
	// The fixture fails a plain clone, as production did.
	if out, err := exec.Command("git", "clone", "--quiet", "--no-checkout", "--shared", "--", r.Folder, filepath.Join(t.TempDir(), "c")).CombinedOutput(); err == nil {
		t.Fatalf("fixture: git clone read an unreadable branch tip:\n%s", out)
	}
	out, err := EnvironmentExecutor{Leases: newFakeLeases(t)}.Run(context.Background(), envRequest(r, "scripts/ok.sh", 60*time.Second))
	if err != nil || out.ExitCode != 0 {
		t.Fatalf("run: %v exit %d\n%s", err, out.ExitCode, out.Output)
	}
	got := string(out.Output)
	for _, want := range []string{"git clone of the folder's repository failed", "shared clone set up by hand", "fallback origin-head", "all good"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output lacks %q:\n%s", want, got)
		}
	}
	if out.Repository == nil || out.Repository.Problem != RepositoryObjectUnreadable || out.Repository.Object != r.Head || out.Repository.Fallback != "origin-head" {
		t.Fatalf("repository check = %+v", out.Repository)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0 {
		t.Fatalf("the step changed the folder's object mode to %v", info.Mode().Perm())
	}
}
