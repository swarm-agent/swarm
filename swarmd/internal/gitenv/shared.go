package gitenv

import (
	"os"
	"strconv"
	"strings"
)

// UseUmaskSharing makes every Git command the daemon starts (its own and its
// agents' commands, which inherit its environment) create files and
// directories with plain umask permissions, whatever core.sharedRepository a
// folder sets.
//
// A person's folder added with `install.sh workspace add` is shared with
// core.sharedRepository=group, so the person's own Git keeps new directories
// group-shared (setgid). The daemon runs with RestrictSUIDSGID, which refuses
// a setgid directory: with the folder's setting, Git in the daemon failed to
// create any new directory in such a repository ("could not create leading
// directories ...: Operation not permitted"), including every worktree. The
// daemon does not need setgid: the same folder's default ACLs give both the
// person and Swarm's user access to whatever the daemon creates, and Swarm's
// own worktree storage is private to it.
func UseUmaskSharing() error {
	env := AppendConfig(os.Environ(), "core.sharedRepository", "umask")
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		if key == "GIT_CONFIG_COUNT" || strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return nil
}

// AppendConfig adds one key=value to env in Git's GIT_CONFIG_COUNT /
// GIT_CONFIG_KEY_n / GIT_CONFIG_VALUE_n form, continuing a count env
// already carries; an entry with the same key and value already there is not
// added twice.
func AppendConfig(env []string, key, value string) []string {
	count := 0
	keys := map[string]string{}
	values := map[string]string{}
	out := make([]string, 0, len(env)+3)
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		switch {
		case k == "GIT_CONFIG_COUNT":
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				count = n
			}
			continue
		case strings.HasPrefix(k, "GIT_CONFIG_KEY_"):
			keys[strings.TrimPrefix(k, "GIT_CONFIG_KEY_")] = v
		case strings.HasPrefix(k, "GIT_CONFIG_VALUE_"):
			values[strings.TrimPrefix(k, "GIT_CONFIG_VALUE_")] = v
		}
		out = append(out, kv)
	}
	for i := 0; i < count; i++ {
		n := strconv.Itoa(i)
		if strings.EqualFold(keys[n], key) && values[n] == value {
			return append(out, "GIT_CONFIG_COUNT="+strconv.Itoa(count))
		}
	}
	n := strconv.Itoa(count)
	return append(out, "GIT_CONFIG_KEY_"+n+"="+key, "GIT_CONFIG_VALUE_"+n+"="+value, "GIT_CONFIG_COUNT="+strconv.Itoa(count+1))
}
