package imagegenlog

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrintfRedactsGoogleAPIKeyFromDaemonAndDurableLogs(t *testing.T) {
	logRoot := filepath.Join(t.TempDir(), "logs")
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "xdg-cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "xdg-state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg-config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-run"))
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "state"))
	t.Setenv("CACHE_DIRECTORY", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	t.Setenv("LOGS_DIRECTORY", logRoot)
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))

	var daemonLog bytes.Buffer
	previousOutput := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&daemonLog)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
	})

	secret := "AIzaSyD-test-google-api-key-secret"
	Printf("", "stage=provider_call_error reason=%q", "Post \"https://generativelanguage.googleapis.com/v1beta/models/test:generateContent?key="+secret+"\": dial tcp failed")

	if strings.Contains(daemonLog.String(), secret) {
		t.Fatalf("daemon imagegen log leaked google API key:\n%s", daemonLog.String())
	}
	if !strings.Contains(daemonLog.String(), "key=[REDACTED]") || !strings.Contains(daemonLog.String(), "dial tcp failed") {
		t.Fatalf("daemon imagegen log = %q, want redacted key and failure context", daemonLog.String())
	}

	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(logRoot)+string(filepath.Separator)) {
		t.Fatalf("imagegen log path = %q, want under daemon log root %q", path, logRoot)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read durable imagegen log: %v", err)
	}
	if strings.Contains(string(content), secret) {
		t.Fatalf("durable imagegen log leaked google API key:\n%s", string(content))
	}
	if !strings.Contains(string(content), "key=[REDACTED]") || !strings.Contains(string(content), "dial tcp failed") {
		t.Fatalf("durable imagegen log = %q, want redacted key and failure context", string(content))
	}
}

func TestAppendRedactsSecretLikeFields(t *testing.T) {
	logRoot := filepath.Join(t.TempDir(), "logs")
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "xdg-cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "xdg-state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg-config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-run"))
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "state"))
	t.Setenv("CACHE_DIRECTORY", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	t.Setenv("LOGS_DIRECTORY", logRoot)
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))

	secret := "secret-token-value"
	Append("[swarmd.imagegen] stage=provider_call_error api_key=\"" + secret + "\" x-goog-api-key:" + secret + " access_token=" + secret)

	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(logRoot)+string(filepath.Separator)) {
		t.Fatalf("imagegen log path = %q, want under daemon log root %q", path, logRoot)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read durable imagegen log: %v", err)
	}
	logContent := string(content)
	if strings.Contains(logContent, secret) {
		t.Fatalf("imagegen log leaked secret-like field:\n%s", logContent)
	}
	for _, want := range []string{"api_key=\"[REDACTED]", "x-goog-api-key:[REDACTED]", "access_token=[REDACTED]"} {
		if !strings.Contains(logContent, want) {
			t.Fatalf("imagegen log missing %q after redaction:\n%s", want, logContent)
		}
	}
}
