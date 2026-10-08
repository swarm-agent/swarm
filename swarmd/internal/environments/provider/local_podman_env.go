package provider

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// resolvePodmanEnvironment fills missing session variables for child processes
// only. It never starts a manager, enables linger, or searches another user's
// session. Explicit addresses are retained; discovery uses the effective UID.
func resolvePodmanEnvironment(env []string, uid int, inspect func(string) (os.FileMode, int, error)) ([]string, error) {
	if uid <= 0 {
		return nil, errors.New("local Podman requires a non-root effective user")
	}
	value := func(key string) string {
		var result string
		for _, entry := range env {
			if strings.HasPrefix(entry, key+"=") {
				result = strings.TrimPrefix(entry, key+"=")
			}
		}
		return result
	}
	runtimeDir := value("XDG_RUNTIME_DIR")
	busAddress := value("DBUS_SESSION_BUS_ADDRESS")
	if runtimeDir != "" && busAddress != "" {
		return append([]string(nil), env...), nil
	}
	if runtimeDir == "" {
		runtimeDir = filepath.Join("/run/user", strconv.Itoa(uid))
	}
	if !filepath.IsAbs(runtimeDir) || filepath.Clean(runtimeDir) != runtimeDir {
		return nil, errors.New("local Podman user runtime directory must be an absolute clean path")
	}
	mode, owner, err := inspect(runtimeDir)
	if err != nil || !mode.IsDir() || mode&os.ModeSymlink != 0 || mode.Perm() != 0700 || owner != uid {
		return nil, errors.New("local Podman user runtime directory unavailable or unsafe; host configuration unchanged")
	}
	if busAddress == "" {
		bus := filepath.Join(runtimeDir, "bus")
		mode, owner, err = inspect(bus)
		if err != nil || mode&os.ModeSocket == 0 || mode&os.ModeSymlink != 0 || owner != uid {
			return nil, errors.New("rootless_systemd user bus unavailable or unsafe; host configuration unchanged")
		}
		// D-Bus addresses escape bytes outside the address grammar, including
		// spaces, commas and semicolons in an explicit runtime directory.
		var escaped strings.Builder
		for _, b := range []byte(bus) {
			if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("_-/\\.*", rune(b)) {
				escaped.WriteByte(b)
			} else {
				fmt.Fprintf(&escaped, "%%%02x", b)
			}
		}
		busAddress = "unix:path=" + escaped.String()
	}
	result := make([]string, 0, len(env)+2)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "XDG_RUNTIME_DIR=") && !strings.HasPrefix(entry, "DBUS_SESSION_BUS_ADDRESS=") {
			result = append(result, entry)
		}
	}
	return append(result, "XDG_RUNTIME_DIR="+runtimeDir, "DBUS_SESSION_BUS_ADDRESS="+busAddress), nil
}
