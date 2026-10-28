package main

// Machine policy belongs to the administrator, not the daemon. These paths are
// the installed headless layout shared with containers/headless/app/install.sh.
import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const machineState = "/var/lib/swarm-headless"
const machineUnits = "/usr/local/lib/systemd/system"
const machineDaemon = "swarm-headless.service"
const machineSocket = "/var/lib/swarmd/local-transport/api.sock"

func machineRoot(args []string) error {
	if os.Geteuid() == 0 {
		return nil
	}
	quoted := make([]string, len(args))
	for i, s := range args {
		quoted[i] = "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
	}
	return fmt.Errorf("run as root: sudo swarmctl %s", strings.Join(quoted, " "))
}
func machineRun(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
func machineWrite(path, body string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".swarmctl-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chown(0, 0); err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		_, err = f.WriteString(body)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
func machineLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, s := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(s) != "" {
			lines = append(lines, s)
		}
	}
	return lines, nil
}
func machineContains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
func machineRequest(method, path string, payload, out any) error {
	c, err := newSetupClient(machineSocket)
	if err != nil {
		return err
	}
	c.Timeout = 20 * time.Second
	defer c.CloseIdleConnections()
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(method, "http://swarm"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("daemon request rejected (HTTP %d)", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func machineWorkspaceCheck(path, who string) (*user.User, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("path must be absolute")
	}
	if strings.Contains(path, "..") || strings.ContainsAny(path, "\n\r\t") {
		return nil, fmt.Errorf("path must not contain .. or control characters")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	real = filepath.Clean(real)
	if real != path {
		return nil, fmt.Errorf("%s goes through a link; use %s", path, real)
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if path == "/" || machineContains(strings.Fields("bin boot dev etc lib lib32 lib64 libx32 proc root run sbin snap sys tmp usr"), parts[0]) {
		return nil, fmt.Errorf("%s is a system folder", path)
	}
	if parts[0] == "var" && (len(parts) == 1 || machineContains(strings.Fields("cache lib lock log mail run spool tmp"), parts[1])) {
		return nil, fmt.Errorf("%s is a system folder", path)
	}
	for _, dir := range []string{"/etc/swarmd", "/var/lib/swarmd", "/var/cache/swarmd", "/var/log/swarmd", "/var/lib/swarm", machineState, "/opt/swarm"} {
		if path == dir || strings.HasPrefix(path, dir+"/") {
			return nil, fmt.Errorf("%s is Swarm's own storage", path)
		}
	}
	for _, part := range parts {
		if strings.HasPrefix(part, ".") {
			return nil, fmt.Errorf("%s is a settings or credentials folder", path)
		}
	}
	if who == "" {
		who = machineInvokingUser()
	}
	if who == "" {
		return nil, fmt.Errorf("cannot tell who this is for: run with sudo (or pkexec) from your own account, or pass --for-user NAME")
	}
	if !regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`).MatchString(who) {
		return nil, fmt.Errorf("--for-user must be a user name")
	}
	person, err := user.Lookup(who)
	if err != nil {
		return nil, err
	}
	uid, err := strconv.Atoi(person.Uid)
	if err != nil || uid < 1000 || uid == 65534 || who == "swarm" {
		return nil, fmt.Errorf("%s is not a person's account; pass --for-user NAME", who)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("cannot determine folder owner")
	}
	owner, err := user.LookupId(strconv.FormatUint(uint64(stat.Uid), 10))
	if err != nil {
		return nil, err
	}
	if owner.Username != who {
		return nil, fmt.Errorf("%s belongs to %s, not %s (pass --for-user %s if it is for them)", path, owner.Username, who, owner.Username)
	}
	home := person.HomeDir
	hi, err := os.Stat(home)
	if err != nil || !filepath.IsAbs(home) || !hi.IsDir() {
		return nil, fmt.Errorf("%s has no home folder", who)
	}
	if path == home || strings.HasPrefix(home, path+"/") {
		return nil, fmt.Errorf("%s is %s's whole home folder (or holds it); add the project folder inside it", path, who)
	}
	passwd, err := exec.Command("getent", "passwd").Output()
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(passwd), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 7 {
			continue
		}
		id, _ := strconv.Atoi(f[2])
		if f[0] != who && id >= 1000 && id != 65534 && len(f[5]) > 1 && filepath.IsAbs(f[5]) && (path == f[5] || strings.HasPrefix(path, f[5]+"/")) {
			return nil, fmt.Errorf("%s is inside %s's home folder", path, f[0])
		}
	}
	return person, nil
}

// machineWorkspaceShareGit runs as the owner: Git must not create root-owned
// configuration or objects while workspace administration is privileged.
//
// Agents work only in a repository with a first commit. A folder that is not
// a repository yet gets an empty one here, as the person: a .git the daemon
// made would belong to Swarm's user, and the person's own Git would refuse
// their folder ("dubious ownership"). Swarm makes the first commit inside it
// later, once the person has chosen what it holds.
func machineWorkspaceShareGit(path, owner string) error {
	if _, err := os.Lstat(filepath.Join(path, ".git")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return machineRun("runuser", "-u", owner, "--", "sh", "-eu", "-c", `
cd "$1"
if [ ! -e .git ]; then
  git init -q --initial-branch=main
fi
git config --local core.sharedRepository group
objects=$(git rev-parse --path-format=absolute --git-path objects)
if [ ! -d "$objects" ] || [ -L "$objects" ]; then
  echo "Git object directory is missing or is a symlink" >&2
  exit 1
fi
find "$objects" -type d -exec chmod g+ws {} +
`, "swarm-workspace-share", path)
}

func machineWorkspaceGrant(path, owner string) error {
	if err := machineWorkspaceShareGit(path, owner); err != nil {
		return err
	}
	if err := machineRun("setfacl", "-R", "-m", "u:swarm:rwX", path); err != nil {
		return err
	}
	if err := machineRun("setfacl", "-R", "-d", "-m", "u:swarm:rwX", "-m", "u:"+owner+":rwX", path); err != nil {
		return err
	}
	for p := filepath.Dir(path); p != "/" && p != "/home"; p = filepath.Dir(p) {
		if exec.Command("runuser", "-u", "swarm", "--", "test", "-x", p).Run() != nil {
			if err := machineRun("setfacl", "-m", "u:swarm:--x", p); err != nil {
				return err
			}
		}
	}
	return nil
}

// machineWorkspaceApply saves the folder list in the unit drop-in (so every
// later start of Swarm sees exactly these folders), then makes the change in
// the running Swarm: mount are the folders to show, unmount the folder to hide.
func machineWorkspaceApply(paths, mount, unmount []string) error {
	drop := filepath.Join(machineUnits, machineDaemon+".d", "50-workspaces.conf")
	// Without an earlier drop-in, the running Swarm started with the
	// installed ProtectHome=yes: its home folders are the shared inaccessible
	// node, and only a restart shows them as the private tmpfs.
	before, _ := os.ReadFile(drop)
	homeShown := strings.Contains(string(before), "\nProtectHome=tmpfs\n")
	if len(paths) == 0 {
		if err := os.Remove(drop); err != nil && !os.IsNotExist(err) {
			return err
		}
		_ = os.Remove(filepath.Dir(drop))
	} else {
		body := "# Written by swarmctl workspace; each folder was added by this machine's administrator.\n[Service]\nProtectHome=tmpfs\n"
		for _, p := range paths {
			body += "BindPaths=" + p + "\nReadWritePaths=" + p + "\n"
		}
		if err := machineWrite(drop, body, 0644); err != nil {
			return err
		}
	}
	return machineWorkspaceTakeEffect(mount, unmount, homeShown)
}

// machineHomeRoots are the folders ProtectHome= replaces in Swarm's view.
var machineHomeRoots = []string{"/home", "/root", "/run/user"}

// machineWorkspaceHomeRoot is the ProtectHome= folder p sits under, or "".
func machineWorkspaceHomeRoot(p string) string {
	for _, r := range machineHomeRoots {
		if p == r || strings.HasPrefix(p, r+"/") {
			return r
		}
	}
	return ""
}

// machineWorkspaceTakeEffect reloads the saved unit and changes the running
// Swarm in place: each new folder is bind-mounted into Swarm's own mount
// namespace at its real path (what BindPaths= does at start), each removed one
// is unmounted there. Agent sandboxes are started by Swarm from inside that
// namespace, so every sandbox started afterwards sees the same folders. Only
// when that is impossible is Swarm restarted, on the wait-for-idle path.
// homeShown: the running Swarm was started with ProtectHome=tmpfs (an earlier
// drop-in), so folders under home can be mounted into it.
func machineWorkspaceTakeEffect(mount, unmount []string, homeShown bool) error {
	if err := machineRun("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if exec.Command("systemctl", "is-active", "-q", machineDaemon).Run() != nil {
		fmt.Println("Swarm is not running; the folder list takes effect when it starts.")
		return nil
	}
	var err error
	if !homeShown {
		for _, p := range mount {
			if r := machineWorkspaceHomeRoot(p); r != "" {
				err = fmt.Errorf("Swarm was started with %s hidden, and only a restart lets it see folders there", r)
				break
			}
		}
	}
	if err == nil {
		err = machineWorkspaceLive(mount, unmount)
	}
	if err == nil {
		if len(mount) > 0 {
			fmt.Println("Swarm can use the folders now; it was not restarted.")
		} else {
			fmt.Println("Swarm no longer sees the folder; it was not restarted.")
		}
		return nil
	}
	fmt.Printf("The running Swarm could not be changed in place (%v); the change takes effect after Swarm restarts.\n", err)
	return machineWorkspaceRestart()
}

// machineWorkspaceRestart is replaced in tests; it waits until Swarm is idle.
var machineWorkspaceRestart = machineWorkspaceRestartWhenIdle

// machineWorkspaceMountPoint runs inside Swarm's mount namespace. It makes the
// folder's mount point there when it is missing: under the read-only tmpfs
// that hides home folders (ProtectHome=tmpfs) it lifts read-only on that tmpfs
// only for the mkdir and puts it back. Anything else is refused, and the
// caller falls back to a restart.
const machineWorkspaceMountPoint = `
p=$1
[ -d "$p" ] && exit 0
a=$(dirname "$p")
while [ ! -d "$a" ]; do a=$(dirname "$a"); done
m=$(findmnt -n -o TARGET -T "$a")
# Only a tmpfs of its own (FSROOT /): a bind of a shared node such as
# systemd's inaccessible directory must never be written to.
if [ "$(findmnt -rn -o FSTYPE,FSROOT --mountpoint "$m")" != "tmpfs /" ]; then
  echo "$a is not on Swarm's private folder tree" >&2
  exit 1
fi
ro=0
case ",$(findmnt -n -o OPTIONS --mountpoint "$m")," in *,ro,*) ro=1 ;; esac
[ "$ro" = 0 ] || mount -o remount,bind,rw "$m"
rc=0
mkdir -p -m 0755 "$p" || rc=$?
[ "$ro" = 0 ] || mount -o remount,bind,ro "$m"
exit $rc
`

// machineWorkspaceLive changes the running Swarm without a restart, or says
// why it cannot.
func machineWorkspaceLive(mount, unmount []string) error {
	out, err := exec.Command("systemctl", "show", "-p", "MainPID", "--value", machineDaemon).Output()
	if err != nil {
		return fmt.Errorf("cannot read Swarm's process: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		return fmt.Errorf("Swarm's process is not known")
	}
	ns := []string{"--target", strconv.Itoa(pid), "--mount", "--"}
	inside := func(args ...string) *exec.Cmd {
		return exec.Command("nsenter", append(append([]string{}, ns...), args...)...)
	}
	for _, p := range unmount {
		if out, err := inside("umount", p).CombinedOutput(); err != nil {
			return fmt.Errorf("%s could not be unmounted: %s", p, machineFirstLine(out, err))
		}
	}
	// A home folder can be mounted only where ProtectHome=tmpfs gave Swarm a
	// private tmpfs; under ProtectHome=yes it is systemd's shared, mode-0000
	// inaccessible node (a bind, so its FSROOT is not /).
	checked := map[string]bool{}
	for _, p := range mount {
		r := machineWorkspaceHomeRoot(p)
		if r == "" || checked[r] {
			continue
		}
		checked[r] = true
		out, err := inside("findmnt", "-n", "-o", "FSTYPE,FSROOT", "--mountpoint", r).Output()
		if err != nil {
			// Not a mount of its own: Swarm sees the machine's own folder.
			continue
		}
		if strings.Join(strings.Fields(string(out)), " ") != "tmpfs /" {
			return fmt.Errorf("%s is hidden from the running Swarm, and only a restart lets it see folders there", r)
		}
	}
	for _, p := range mount {
		// Already mounted read-write at exactly this path: nothing to do.
		if out, err := inside("findmnt", "-n", "-o", "OPTIONS", "--mountpoint", p).Output(); err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "rw") {
			continue
		}
		if out, err := inside("sh", "-eu", "-c", machineWorkspaceMountPoint, "swarm-mount-point", p).CombinedOutput(); err != nil {
			return fmt.Errorf("no mount point for %s: %s", p, machineFirstLine(out, err))
		}
		// systemctl bind takes the folder from the machine's own view (Swarm's
		// hides home folders) and mounts it read-write at the same path.
		if out, err := exec.Command("systemctl", "bind", machineDaemon, p, p).CombinedOutput(); err != nil {
			return fmt.Errorf("%s could not be mounted: %s", p, machineFirstLine(out, err))
		}
	}
	return nil
}

func machineFirstLine(out []byte, err error) string {
	if s := strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]); s != "" {
		return s
	}
	return err.Error()
}

func machineWorkspaceRestartWhenIdle() error {
	limit := 900
	if s := os.Getenv("RESTART_WAIT_SECONDS"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid RESTART_WAIT_SECONDS")
		}
		limit = n
	}
	for waited := 0; ; waited += 10 {
		var busy struct {
			Busy *bool `json:"busy"`
		}
		err := machineRequest(http.MethodGet, "/v3/box/busy", nil, &busy)
		if err == nil && busy.Busy != nil && !*busy.Busy {
			break
		}
		if waited >= limit {
			return fmt.Errorf("Swarm was not restarted: it is still working or daemon status is unavailable. The change is saved and takes effect when Swarm next restarts; run the same command again once the work is done")
		}
		if waited == 0 {
			fmt.Println("Swarm is working or daemon status is unavailable; waiting until it is idle before restarting it.")
		}
		time.Sleep(10 * time.Second)
		if exec.Command("systemctl", "is-active", "-q", machineDaemon).Run() != nil {
			break
		}
	}
	if err := machineRun("systemctl", "restart", machineDaemon); err != nil {
		return err
	}
	for i := 0; i < 90; i++ {
		var out any
		if machineRequest(http.MethodGet, "/v1/onboarding", nil, &out) == nil {
			fmt.Println("Swarm restarted with the new folder list.")
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("Swarm is not answering; check: journalctl -u %s", machineDaemon)
}
func cmdMachineWorkspace(args []string) error {
	if err := machineRoot(append([]string{"workspace"}, args...)); err != nil {
		return err
	}
	if _, err := exec.LookPath("setfacl"); err != nil {
		return fmt.Errorf("setfacl is missing (apt-get install acl)")
	}
	paths, err := machineLines(machineState + "/workspaces")
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: swarmctl workspace add|remove|list|publish|resolve")
	}
	switch args[0] {
	case "add":
		// Several folders in one call apply once, so an app adding a project's
		// folders asks for one password, not one per folder.
		adds, who, err := machineWorkspaceAddArgs(args[1:])
		if err != nil {
			return err
		}
		owners := make([]string, len(adds))
		for i, p := range adds {
			person, err := machineWorkspaceCheck(p, who)
			if err != nil {
				return err
			}
			owners[i] = person.Username
		}
		changed := false
		for _, p := range adds {
			if !machineContains(paths, p) {
				paths = append(paths, p)
				changed = true
			}
		}
		if changed {
			if err = machineWrite(machineState+"/workspaces", strings.Join(paths, "\n")+"\n", 0644); err != nil {
				return err
			}
		}
		for i, p := range adds {
			if err = machineWorkspaceGrant(p, owners[i]); err != nil {
				return err
			}
		}
		if err = machineWorkspaceApply(paths, adds, nil); err != nil {
			return err
		}
		for i, p := range adds {
			fmt.Printf("%s is visible to Swarm (owner %s). Register it in place: swarm_add_workspace %s\n", p, owners[i], p)
		}
	case "remove":
		if len(args) != 2 {
			return fmt.Errorf("usage: swarmctl workspace remove PATH")
		}
		p := args[1]
		if !filepath.IsAbs(p) || strings.Contains(p, "..") {
			return fmt.Errorf("path must be absolute")
		}
		if !machineContains(paths, p) {
			return fmt.Errorf("%s is not a Swarm workspace folder on this machine", p)
		}
		var kept []string
		for _, v := range paths {
			if v != p {
				kept = append(kept, v)
			}
		}
		body := ""
		if len(kept) > 0 {
			body = strings.Join(kept, "\n") + "\n"
		}
		if err = machineWrite(machineState+"/workspaces", body, 0644); err != nil {
			return err
		}
		if info, e := os.Stat(p); e == nil && info.IsDir() {
			// Removal is best-effort, as in the installer: existing ACLs may be absent.
			_ = exec.Command("setfacl", "-R", "-x", "u:swarm", p).Run()
			_ = exec.Command("setfacl", "-R", "-d", "-x", "u:swarm", p).Run()
			if st, ok := info.Sys().(*syscall.Stat_t); ok {
				_ = exec.Command("setfacl", "-R", "-d", "-x", fmt.Sprintf("u:%d", st.Uid), p).Run()
			}
		}
		if err = machineWorkspaceApply(kept, nil, []string{p}); err != nil {
			return err
		}
		fmt.Printf("%s is no longer visible to Swarm.\n", p)
	case "list":
		if len(args) != 1 {
			return fmt.Errorf("usage: swarmctl workspace list")
		}
		for _, p := range paths {
			fmt.Println(p)
		}
		rules, e := machineLines(machineState + "/publish")
		if e != nil {
			return e
		}
		if len(rules) > 0 {
			fmt.Println("Delivered as pull requests:")
			for _, r := range rules {
				fmt.Println("  " + r)
			}
		}
	case "publish":
		return machinePublish(args[1:], paths)
	default:
		return fmt.Errorf("usage: swarmctl workspace add|remove|list|publish|resolve")
	}
	return nil
}
func machinePublish(args, paths []string) error {
	usage := fmt.Errorf("usage: swarmctl workspace publish PATH COMMAND [--sync] | publish PATH --off")
	if len(args) < 2 {
		return usage
	}
	path, command := args[0], args[1]
	if !machineContains(paths, path) {
		return fmt.Errorf("%s is not a Swarm workspace folder on this machine (swarmctl workspace add %s)", path, path)
	}
	var options []string
	for _, flag := range args[2:] {
		if flag != "--sync" {
			return usage
		}
		options = append(options, strings.TrimPrefix(flag, "--"))
	}
	if command == "--off" {
		if len(options) > 0 {
			return usage
		}
	} else {
		info, err := os.Stat(command)
		if err != nil || !filepath.IsAbs(command) || strings.Contains(command, "..") || strings.ContainsAny(command, "\t\r\n") || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("COMMAND must be an absolute path to an executable file")
		}
		if strings.HasPrefix(command, "/home/") || strings.HasPrefix(command, "/root/") {
			return fmt.Errorf("COMMAND must be outside home folders (Swarm cannot see them); /usr/local/lib is a good place")
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 || info.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("COMMAND must be owned by root and writable only by root")
		}
	}
	if command != "--off" {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("cannot determine folder owner")
		}
		owner, err := user.LookupId(strconv.FormatUint(uint64(st.Uid), 10))
		if err != nil {
			return err
		}
		if err := machineWorkspaceShareGit(path, owner.Username); err != nil {
			return err
		}
	}
	rules, err := machineLines(machineState + "/publish")
	if err != nil {
		return err
	}
	var kept []string
	for _, r := range rules {
		if strings.SplitN(r, "\t", 2)[0] != path {
			kept = append(kept, r)
		}
	}
	if command != "--off" {
		r := path + "\t" + command
		if len(options) > 0 {
			r += "\t" + strings.Join(options, ",")
		}
		kept = append(kept, r)
	}
	body := ""
	if len(kept) > 0 {
		body = strings.Join(kept, "\n") + "\n"
	}
	if err = machineWrite(machineState+"/publish", body, 0644); err != nil {
		return err
	}
	if command == "--off" {
		fmt.Printf("Jobs in %s are merged into the folder again.\n", path)
		return nil
	}
	fmt.Printf("Jobs in %s use publish command %s (%s).\n", path, command, strings.Join(options, ","))
	return nil
}

func machineMinutes(s string) bool { return regexp.MustCompile(`^[1-9][0-9]{0,5}$`).MatchString(s) }
func machineRemove(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
func cmdIdleOff(args []string) error {
	if err := machineRoot(append([]string{"idle-off"}, args...)); err != nil {
		return err
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: swarmctl idle-off MINUTES|off|status")
	}
	switch args[0] {
	case "check":
		return machineIdleCheck()
	case "status":
		b, err := os.ReadFile(machineState + "/idle-off-minutes")
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		s := strings.TrimSpace(string(b))
		if s == "" {
			s = "off"
		}
		fmt.Printf("Idle auto-off: %s (minutes when enabled)\n", s)
		out, _ := exec.Command("systemctl", "--no-pager", "status", "swarm-idle-off.timer").CombinedOutput()
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "●") || strings.Contains(line, "Active:") {
				fmt.Println(line)
			}
		}
		return nil
	case "off":
		if err := machineRun("systemctl", "disable", "--now", "swarm-idle-off.timer"); err != nil {
			return err
		}
		if err := machineRemove(machineState + "/idle-off-minutes"); err != nil {
			return err
		}
		return machineRemove(machineState + "/idle-off-since")
	}
	if !machineMinutes(args[0]) {
		return fmt.Errorf("usage: swarmctl idle-off MINUTES|off|status")
	}
	if err := machineWrite(machineState+"/idle-off-minutes", args[0]+"\n", 0644); err != nil {
		return err
	}
	if err := machineRemove(machineState + "/idle-off-since"); err != nil {
		return err
	}
	// The timer calls Go directly; no copied installer or helper script is needed.
	if err := machineWrite(machineUnits+"/swarm-idle-off.service", "[Unit]\nDescription=Power off Swarm host after continuous verified idle\n[Service]\nType=oneshot\nUser=root\nExecStart=/usr/local/bin/swarmctl idle-off check\n", 0644); err != nil {
		return err
	}
	if err := machineWrite(machineUnits+"/swarm-idle-off.timer", "[Unit]\nDescription=Check opt-in Swarm host idle policy\n[Timer]\nOnBootSec=1min\nOnUnitActiveSec=1min\n[Install]\nWantedBy=timers.target\n", 0644); err != nil {
		return err
	}
	if err := machineRun("systemctl", "daemon-reload"); err != nil {
		return err
	}
	return machineRun("systemctl", "enable", "--now", "swarm-idle-off.timer")
}
func machineIdleResponse(v map[string]json.RawMessage) bool {
	if string(v["busy"]) != "false" {
		return false
	}
	for _, k := range []string{"sessions", "worker_runs", "followups", "reviews"} {
		if _, ok := v[k]; !ok {
			return false
		}
	}
	for k, raw := range v {
		if k == "busy" {
			continue
		}
		var n float64
		if string(raw) == "null" || json.Unmarshal(raw, &n) != nil || n != 0 {
			return false
		}
	}
	return true
}
func machineIdleCheck() error {
	b, err := os.ReadFile(machineState + "/idle-off-minutes")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	s := strings.TrimSpace(string(b))
	if !machineMinutes(s) {
		return nil
	}
	minutes, _ := strconv.ParseInt(s, 10, 64)
	reset := func() error { return machineRemove(machineState + "/idle-off-since") }
	var busy map[string]json.RawMessage
	if machineRequest(http.MethodGet, "/v3/box/busy", nil, &busy) != nil || !machineIdleResponse(busy) {
		return reset()
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return err
	}
	uptime, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return err
	}
	fields := strings.Fields(string(uptime))
	if len(fields) == 0 {
		return fmt.Errorf("cannot read uptime")
	}
	now, err := strconv.ParseInt(strings.SplitN(fields[0], ".", 2)[0], 10, 64)
	if err != nil {
		return err
	}
	bootID := strings.TrimSpace(string(boot))
	since := now
	saved, err := os.ReadFile(machineState + "/idle-off-since")
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	f := strings.Fields(string(saved))
	if len(f) == 2 && f[0] == bootID {
		n, e := strconv.ParseInt(f[1], 10, 64)
		if e == nil && n >= 0 && n <= now {
			since = n
		}
	}
	if err = machineWrite(machineState+"/idle-off-since", fmt.Sprintf("%s %d\n", bootID, since), 0644); err != nil {
		return err
	}
	if now-since < minutes*60 {
		return nil
	}
	if machineRequest(http.MethodPost, "/v3/signals", map[string]string{"kind": "external.idle_off", "severity": "info", "summary": "swarmctl powering off idle host", "source": "swarmctl"}, nil) != nil {
		return reset()
	}
	if err = reset(); err != nil {
		return err
	}
	return machineRun("systemctl", "poweroff")
}

// machineWorkspaceAddArgs reads `add PATH [PATH...] [--for-user NAME]`: one or
// more absolute folders (each once) and an optional owner, in any order.
func machineWorkspaceAddArgs(args []string) ([]string, string, error) {
	usage := fmt.Errorf("usage: swarmctl workspace add PATH [PATH...] [--for-user NAME]")
	var adds []string
	who := ""
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--for-user":
			if who != "" || i+1 >= len(args) {
				return nil, "", usage
			}
			who = args[i+1]
			i++
		case strings.HasPrefix(a, "-"):
			return nil, "", usage
		default:
			if !machineContains(adds, a) {
				adds = append(adds, a)
			}
		}
	}
	if len(adds) == 0 {
		return nil, "", usage
	}
	return adds, who, nil
}

// machineInvokingUser is the account that asked for a root command: sudo sets
// SUDO_USER, pkexec (the desktop password prompt apps use) sets PKEXEC_UID.
func machineInvokingUser() string {
	if who := strings.TrimSpace(os.Getenv("SUDO_USER")); who != "" {
		return who
	}
	if uid := strings.TrimSpace(os.Getenv("PKEXEC_UID")); uid != "" {
		if _, err := strconv.Atoi(uid); err == nil {
			if u, err := user.LookupId(uid); err == nil {
				return u.Username
			}
		}
	}
	return ""
}
