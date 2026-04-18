package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	lifecycleKindDirect  = "direct"
	lifecycleKindSystemd = "systemd"
)

type lifecycleManager struct {
	Kind      string `json:"kind"`
	Scope     string `json:"scope,omitempty"`
	Unit      string `json:"unit,omitempty"`
	Source    string `json:"source,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type daemonLockState struct {
	PID int `json:"pid"`
}

func normalizeLifecycleManager(manager lifecycleManager) (lifecycleManager, bool) {
	manager.Kind = strings.ToLower(strings.TrimSpace(manager.Kind))
	manager.Scope = string(normalizeSystemdScope(manager.Scope))
	manager.Unit = strings.TrimSpace(manager.Unit)
	manager.Source = strings.TrimSpace(manager.Source)
	manager.UpdatedAt = strings.TrimSpace(manager.UpdatedAt)
	switch manager.Kind {
	case lifecycleKindDirect:
		manager.Scope = ""
		manager.Unit = ""
		return manager, true
	case lifecycleKindSystemd:
		if manager.Scope == "" || manager.Unit == "" {
			return lifecycleManager{}, false
		}
		return manager, true
	default:
		return lifecycleManager{}, false
	}
}

func writeLifecycleManager(profile Profile, manager lifecycleManager) error {
	normalized, ok := normalizeLifecycleManager(manager)
	if !ok {
		return fmt.Errorf("invalid lifecycle manager metadata")
	}
	normalized.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := os.MkdirAll(filepath.Dir(profile.ManagerFile), 0o755); err != nil {
		return fmt.Errorf("create lifecycle metadata directory: %w", err)
	}
	data, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal lifecycle metadata: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(profile.ManagerFile, data, 0o644); err != nil {
		return fmt.Errorf("write lifecycle metadata: %w", err)
	}
	return nil
}

func readLifecycleManager(profile Profile) (lifecycleManager, bool, error) {
	data, err := os.ReadFile(profile.ManagerFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return lifecycleManager{}, false, nil
		}
		return lifecycleManager{}, false, fmt.Errorf("read lifecycle metadata: %w", err)
	}
	var manager lifecycleManager
	if err := json.Unmarshal(data, &manager); err != nil {
		return lifecycleManager{}, false, fmt.Errorf("parse lifecycle metadata: %w", err)
	}
	normalized, ok := normalizeLifecycleManager(manager)
	if !ok {
		return lifecycleManager{}, false, nil
	}
	return normalized, true, nil
}

func recordCurrentLifecycleManager(profile Profile) error {
	manager, err := detectCurrentLifecycleManager()
	if err != nil {
		return err
	}
	return writeLifecycleManager(profile, manager)
}

func detectCurrentLifecycleManager() (lifecycleManager, error) {
	if manager, ok, err := detectLifecycleManagerForPID(os.Getpid()); err != nil {
		return lifecycleManager{}, err
	} else if ok {
		return manager, nil
	}
	return lifecycleManager{Kind: lifecycleKindDirect, Source: "launcher"}, nil
}

func resolveLifecycleManager(profile Profile) (lifecycleManager, bool, error) {
	pid, running, err := runningDaemonPID(profile)
	if err != nil {
		return lifecycleManager{}, false, err
	}
	if running {
		if manager, ok, err := detectLifecycleManagerForPID(pid); err != nil {
			return lifecycleManager{}, false, err
		} else if ok {
			if err := writeLifecycleManager(profile, manager); err != nil {
				return lifecycleManager{}, false, err
			}
			return manager, true, nil
		}
		if pidText := ReadPIDFile(profile); strings.TrimSpace(pidText) != "" {
			if parsed, parseErr := strconv.Atoi(strings.TrimSpace(pidText)); parseErr == nil && parsed == pid {
				manager := lifecycleManager{Kind: lifecycleKindDirect, Source: "pidfile"}
				if err := writeLifecycleManager(profile, manager); err != nil {
					return lifecycleManager{}, false, err
				}
				return manager, true, nil
			}
		}
	}
	return readLifecycleManager(profile)
}

func runningDaemonPID(profile Profile) (int, bool, error) {
	if pid, ok, err := readLockPID(profile.LockPath); err != nil {
		return 0, false, err
	} else if ok && processRunning(strconv.Itoa(pid)) {
		return pid, true, nil
	}
	pidText := strings.TrimSpace(ReadPIDFile(profile))
	if pidText == "" || !processRunning(pidText) {
		return 0, false, nil
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 {
		return 0, false, nil
	}
	return pid, true, nil
}

func readLockPID(path string) (int, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read daemon lock: %w", err)
	}
	var state daemonLockState
	if err := json.Unmarshal(data, &state); err != nil {
		text := strings.TrimSpace(string(data))
		if pid, parseErr := strconv.Atoi(text); parseErr == nil && pid > 0 {
			return pid, true, nil
		}
		return 0, false, fmt.Errorf("parse daemon lock: %w", err)
	}
	if state.PID <= 0 {
		return 0, false, nil
	}
	return state.PID, true, nil
}

func detectLifecycleManagerForPID(pid int) (lifecycleManager, bool, error) {
	if pid <= 0 || runtime.GOOS != "linux" {
		return lifecycleManager{}, false, nil
	}
	env, envErr := readProcEnviron(pid)
	if envErr == nil {
		if unit := strings.TrimSpace(env["SWARM_SYSTEMD_UNIT"]); unit != "" {
			scope := normalizeSystemdScope(env["SWARM_SYSTEMD_SCOPE"])
			if scope == "" {
				scope, _, _ = detectSystemdService(unit)
			}
			if scope != "" {
				return lifecycleManager{
					Kind:   lifecycleKindSystemd,
					Scope:  string(scope),
					Unit:   unit,
					Source: "proc-environ",
				}, true, nil
			}
		}
	}
	if envErr != nil && !errors.Is(envErr, os.ErrNotExist) && !errors.Is(envErr, os.ErrPermission) {
		return lifecycleManager{}, false, fmt.Errorf("read process environment: %w", envErr)
	}
	scope, unit, ok, err := detectSystemdUnitFromProcCgroup(pid)
	if err != nil {
		return lifecycleManager{}, false, err
	}
	if ok {
		return lifecycleManager{
			Kind:   lifecycleKindSystemd,
			Scope:  string(scope),
			Unit:   unit,
			Source: "proc-cgroup",
		}, true, nil
	}
	return lifecycleManager{}, false, nil
}

func readProcEnviron(pid int) (map[string]string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil, err
	}
	env := make(map[string]string)
	for _, entry := range strings.Split(string(data), "\x00") {
		if entry == "" {
			continue
		}
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		env[key] = value
	}
	return env, nil
}

func detectSystemdUnitFromProcCgroup(pid int) (systemdServiceScope, string, bool, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", false, nil
		}
		return "", "", false, fmt.Errorf("read process cgroup: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		_, cgroupPath, ok := strings.Cut(line, "::")
		if !ok {
			parts := strings.SplitN(line, ":", 3)
			if len(parts) != 3 {
				continue
			}
			cgroupPath = parts[2]
		}
		if scope, unit, matched := parseSystemdUnitFromCgroupPath(cgroupPath); matched {
			return scope, unit, true, nil
		}
	}
	return "", "", false, nil
}

func parseSystemdUnitFromCgroupPath(path string) (systemdServiceScope, string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", "", false
	}
	parts := strings.Split(path, "/")
	userScope := false
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "user-") && strings.HasSuffix(part, ".slice") {
			userScope = true
		}
	}
	for i := len(parts) - 1; i >= 0; i-- {
		part := strings.TrimSpace(parts[i])
		if part == "" || !strings.HasSuffix(part, ".service") {
			continue
		}
		if strings.HasPrefix(part, "user@") {
			continue
		}
		scope := systemdServiceSystem
		if userScope {
			scope = systemdServiceUser
		}
		return scope, part, true
	}
	return "", "", false
}

func normalizeSystemdScope(value string) systemdServiceScope {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(systemdServiceUser):
		return systemdServiceUser
	case string(systemdServiceSystem):
		return systemdServiceSystem
	default:
		return ""
	}
}
