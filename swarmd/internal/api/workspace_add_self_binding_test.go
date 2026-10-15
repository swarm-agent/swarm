package api

import (
	"fmt"

	"os"
	"os/exec"
)

func ensureTestWorkspaceDir(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	if output, err := exec.Command("git", "init", "--initial-branch=main", path).CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %w: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", path, "-c", "user.name=Swarm Test", "-c", "user.email=swarm-test@localhost", "commit", "--allow-empty", "--no-gpg-sign", "-m", "Initial commit").CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w: %s", err, output)
	}
	return nil
}
