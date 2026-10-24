package sandbox

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func (m *Manager) leaseContainerAsDaemon(name string) bool {
	t := m.leaseTable()
	t.mu.Lock()
	daemon := false
	for _, l := range t.byID {
		if l.Container == name {
			daemon = l.asDaemon
			break
		}
	}
	t.mu.Unlock()
	return daemon
}

func (m *Manager) leaseGroupArgs(name, root string) []string {
	if !m.leaseContainerAsDaemon(name) {
		return nil
	}
	// gVisor ignores POSIX ACLs; the ACL already grants this user the folder,
	// so using its Git directory's group grants no new access.
	path := filepath.Join(root, ".git")
	st, err := os.Lstat(path)
	if err != nil || st.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if st.Mode().IsRegular() {
		data, err := os.ReadFile(path)
		if err != nil || !strings.HasPrefix(string(data), "gitdir: ") {
			return nil
		}
		path = strings.TrimSpace(strings.TrimPrefix(string(data), "gitdir: "))
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		st, err = os.Lstat(path)
		if err != nil || !st.IsDir() {
			return nil
		}
		common, err := os.ReadFile(filepath.Join(path, "commondir"))
		if err == nil {
			dir := strings.TrimSpace(string(common))
			if dir == "" {
				return nil
			}
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(path, dir)
			}
			st, err = os.Lstat(dir)
			if err != nil {
				return nil
			}
		} else if !os.IsNotExist(err) {
			return nil
		}
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Gid) == m.cfg.GID {
		return nil
	}
	return []string{"--group-add", strconv.FormatUint(uint64(stat.Gid), 10)}
}

// Include daemon lease credentials so recovered containers are recreated when
// arguments change. Owner credentials are already hashed by specHashImage; keep
// their existing spec unchanged to avoid disrupting running owner containers.
func (m *Manager) leaseSpec(name, root, spec string) string {
	if !m.leaseContainerAsDaemon(name) {
		return spec
	}
	uid, gid, _ := m.containerUser(name, root)
	return spec + "-user-" + strconv.Itoa(uid) + "-" + strconv.Itoa(gid) + "-groups-" + strings.Join(m.leaseGroupArgs(name, root), "-")
}
