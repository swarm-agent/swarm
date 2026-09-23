package security

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var (
	secretsMu sync.RWMutex
)

// SecretsFilePath returns the canonical path to the host user's private secrets file.
// It defaults to ~/.config/swarm/secrets.env (mode 0600), outside any git workspace.
func SecretsFilePath() string {
	if custom := strings.TrimSpace(os.Getenv("SWARM_SECRETS_FILE")); custom != "" {
		return custom
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".config", "swarm", "secrets.env")
}

// GetLocalSecret retrieves a secret by name.
// It checks process environment variables first, then reads ~/.config/swarm/secrets.env.
func GetLocalSecret(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("secret name cannot be empty")
	}

	// 1. Process environment lookup
	if val, ok := os.LookupEnv(name); ok && val != "" {
		return val, nil
	}

	// 2. File lookup in ~/.config/swarm/secrets.env
	path := SecretsFilePath()
	secretsMu.RLock()
	defer secretsMu.RUnlock()

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read secrets file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.TrimSpace(parts[1])
			// Strip surrounding quotes if present
			if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
				v = v[1 : len(v)-1]
			}
			if k == name {
				return v, nil
			}
		}
	}
	return "", scanner.Err()
}

// SetLocalSecret writes or updates a secret in ~/.config/swarm/secrets.env with mode 0600.
func SetLocalSecret(name, value string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("secret name cannot be empty")
	}

	path := SecretsFilePath()
	dir := filepath.Dir(path)

	secretsMu.Lock()
	defer secretsMu.Unlock()

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create secrets directory: %w", err)
	}

	existingLines := []string{}
	found := false

	if data, err := os.ReadFile(path); err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			line := scanner.Text()
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				parts := strings.SplitN(trimmed, "=", 2)
				if len(parts) == 2 && strings.TrimSpace(parts[0]) == name {
					existingLines = append(existingLines, fmt.Sprintf("%s=%s", name, value))
					found = true
					continue
				}
			}
			existingLines = append(existingLines, line)
		}
	}

	if !found {
		existingLines = append(existingLines, fmt.Sprintf("%s=%s", name, value))
	}

	content := strings.Join(existingLines, "\n") + "\n"
	return os.WriteFile(path, []byte(content), 0600)
}

// ListLocalSecretNames returns a sorted list of configured secret names (WITHOUT values).
func ListLocalSecretNames() ([]string, error) {
	path := SecretsFilePath()
	secretsMu.RLock()
	defer secretsMu.RUnlock()

	namesMap := make(map[string]struct{})

	f, err := os.Open(path)
	if err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				k := strings.TrimSpace(parts[0])
				if k != "" {
					namesMap[k] = struct{}{}
				}
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	names := make([]string, 0, len(namesMap))
	for k := range namesMap {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, nil
}
