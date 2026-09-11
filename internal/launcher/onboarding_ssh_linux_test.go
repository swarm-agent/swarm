//go:build linux

package launcher

import (
	"bytes"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// Requirement: writeOnboardingSSHKey must never follow symlink/hardlink targets
// or overwrite observed concurrent changes. Real temp-dir syscalls are the
// narrowest hermetic proof of mode/owner, atomic failure, preservation and retry;
// the test runs as the test user and never touches an OS account or real keys.
func TestOnboardingSSHFilesystem(t *testing.T) {
	for _, scenario := range []string{"new", "existing", "duplicate", "symlink file", "symlink dir", "ancestor symlink", "hardlink", "fifo", "writable file", "writable dir", "write failure", "short write", "file race", "dir race", "identity race", "wrong owner", "home race", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			ssh := filepath.Join(home, ".ssh")
			if err := os.Mkdir(ssh, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(ssh, "authorized_keys")
			original := []byte("# keep exactly\n" + testSSHKey(9) + " original-comment")
			key := []byte(testSSHKey(8) + "\n")
			if scenario == "duplicate" {
				original = append(original, '\n')
				original = append(original, key...)
			}
			if scenario != "new" {
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
			}
			outside := filepath.Join(root, "unrelated")
			if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			hooks := sshWriteHooks{}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "symlink file":
				must(os.Remove(path))
				must(os.Symlink(outside, path))
			case "symlink dir":
				must(os.Rename(ssh, ssh+"-original"))
				must(os.Symlink(ssh+"-original", ssh))
			case "ancestor symlink":
				must(os.Symlink(home, filepath.Join(root, "alias")))
				home = filepath.Join(root, "alias")
			case "hardlink":
				must(os.Remove(path))
				must(os.Link(outside, path))
			case "fifo":
				must(os.Remove(path))
				must(syscall.Mkfifo(path, 0600))
			case "writable file":
				must(os.Chmod(path, 0666))
			case "writable dir":
				must(os.Chmod(ssh, 0777))
			case "write failure":
				hooks.write = func(*os.File, []byte) (int, error) { return 0, errors.New("injected write failure") }
			case "short write":
				hooks.write = func(*os.File, []byte) (int, error) { return 0, nil }
			case "oversized":
				must(os.WriteFile(path, make([]byte, maxAuthorizedKeys+1), 0600))
			case "home race":
				hooks.beforeCommit = func() { must(os.Rename(home, home+"-moved")); must(os.Mkdir(home, 0700)) }
			case "file race":
				hooks.beforeCommit = func() { must(os.WriteFile(path, []byte("concurrent key"), 0600)) }
			case "dir race":
				hooks.beforeCommit = func() {
					must(os.Rename(ssh, ssh+"-moved"))
					must(os.Mkdir(ssh, 0700))
					must(os.WriteFile(path, []byte("concurrent key"), 0600))
				}
			}
			u := &user.User{Username: "fixture", Uid: strconv.Itoa(os.Getuid()), Gid: strconv.Itoa(os.Getgid()), HomeDir: home}
			if scenario == "wrong owner" {
				u.Uid = strconv.Itoa(os.Getuid() + 1)
			}
			checks := 0
			validate := func() error {
				checks++
				if scenario == "identity race" && checks > 1 {
					return errors.New("identity changed")
				}
				return nil
			}
			err := writeOnboardingSSHKey(u, key, validate, hooks)
			success := scenario == "new" || scenario == "existing" || scenario == "duplicate"
			if (err == nil) != success {
				t.Fatalf("unexpected result %v", err)
			}
			if b, e := os.ReadFile(outside); e != nil || string(b) != "untouched" {
				t.Fatal("unrelated file changed")
			}
			if success {
				b, e := os.ReadFile(path)
				must(e)
				expected := appendOnboardingKey(original, key)
				if scenario == "new" {
					expected = key
				}
				if !bytes.Equal(b, expected) {
					t.Fatal("existing bytes lost")
				}
				for p, mode := range map[string]os.FileMode{ssh: 0700, path: 0600} {
					info, e := os.Stat(p)
					must(e)
					st := info.Sys().(*syscall.Stat_t)
					if info.Mode().Perm() != mode || st.Uid != uint32(os.Getuid()) || st.Gid != uint32(os.Getgid()) {
						t.Fatal("wrong metadata")
					}
				}
				must(writeOnboardingSSHKey(u, key, validate, sshWriteHooks{}))
				again, e := os.ReadFile(path)
				must(e)
				if !bytes.Equal(b, again) {
					t.Fatal("retry duplicated key")
				}
			} else if scenario == "file race" || scenario == "dir race" {
				b, e := os.ReadFile(path)
				must(e)
				if string(b) != "concurrent key" {
					t.Fatal("race clobbered")
				}
			} else if scenario == "write failure" || scenario == "short write" || scenario == "identity race" || scenario == "writable file" || scenario == "writable dir" {
				b, e := os.ReadFile(path)
				must(e)
				if !bytes.Equal(b, original) {
					t.Fatal("failure clobbered original")
				}
			}
		})
	}
}

// Requirement: production filesystem credential isolation restores the thread
// and process credentials even on failure. Exercise the real wrapper against
// the current unprivileged test user's temporary home, never another account.
func TestOnboardingSSHFilesystemIdentity(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("unprivileged wrapper fixture; root transition requires separately authorized live proof")
	}
	u := &user.User{Username: "fixture", Uid: strconv.Itoa(os.Getuid()), Gid: strconv.Itoa(os.Getgid()), HomeDir: t.TempDir()}
	if err := installOnboardingSSHKey(u, []byte(testSSHKey(1)+"\n"), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := installOnboardingSSHKey(u, []byte(testSSHKey(2)+"\n"), func() error { return errors.New("injected validation failure") }); err == nil {
		t.Fatal("validation failure hidden")
	}
	b, err := os.ReadFile(filepath.Join(u.HomeDir, ".ssh", "authorized_keys"))
	if err != nil || string(b) != testSSHKey(1)+"\n" {
		t.Fatal("failure changed key")
	}
}
