package provider

import (
	"os"
	"syscall"
)

func localPodmanEnvironment() ([]string, error) {
	return resolvePodmanEnvironment(os.Environ(), os.Geteuid(), func(path string) (os.FileMode, int, error) {
		info, err := os.Lstat(path)
		if err != nil {
			return 0, -1, err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return info.Mode(), -1, nil
		}
		return info.Mode(), int(stat.Uid), nil
	})
}
