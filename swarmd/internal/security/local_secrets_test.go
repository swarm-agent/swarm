package security

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalSecretsStorage(t *testing.T) {
	tempDir := t.TempDir()
	secretsFile := filepath.Join(tempDir, "secrets.env")
	t.Setenv("SWARM_SECRETS_FILE", secretsFile)

	// 1. Initially empty
	names, err := ListLocalSecretNames()
	if err != nil {
		t.Fatalf("unexpected error listing secrets: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("expected 0 secrets, got %d", len(names))
	}

	// 2. Set secret
	if err := SetLocalSecret("TWITTER_API_KEY", "super-secret-key-123"); err != nil {
		t.Fatalf("failed to set secret: %v", err)
	}

	// Verify permissions (0600)
	info, err := os.Stat(secretsFile)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Fatalf("expected file permission 0600, got %o", perm)
	}

	// 3. Get secret
	val, err := GetLocalSecret("TWITTER_API_KEY")
	if err != nil {
		t.Fatalf("failed to get secret: %v", err)
	}
	if val != "super-secret-key-123" {
		t.Fatalf("expected 'super-secret-key-123', got %q", val)
	}

	// 4. Set second secret and update first
	if err := SetLocalSecret("SLACK_WEBHOOK_URL", "https://hooks.slack.com/services/xyz"); err != nil {
		t.Fatalf("failed to set second secret: %v", err)
	}
	if err := SetLocalSecret("TWITTER_API_KEY", "updated-key-456"); err != nil {
		t.Fatalf("failed to update secret: %v", err)
	}

	valUpdated, err := GetLocalSecret("TWITTER_API_KEY")
	if err != nil || valUpdated != "updated-key-456" {
		t.Fatalf("expected updated value 'updated-key-456', got %q (err: %v)", valUpdated, err)
	}

	// 5. List secret names (names only, no values)
	names, err = ListLocalSecretNames()
	if err != nil {
		t.Fatalf("failed to list names: %v", err)
	}
	if len(names) != 2 || names[0] != "SLACK_WEBHOOK_URL" || names[1] != "TWITTER_API_KEY" {
		t.Fatalf("expected [SLACK_WEBHOOK_URL, TWITTER_API_KEY], got %+v", names)
	}

	// 6. Environment variable takes precedence over file
	t.Setenv("TWITTER_API_KEY", "env-precedence-key")
	envVal, err := GetLocalSecret("TWITTER_API_KEY")
	if err != nil || envVal != "env-precedence-key" {
		t.Fatalf("expected env precedence 'env-precedence-key', got %q (err: %v)", envVal, err)
	}
}
