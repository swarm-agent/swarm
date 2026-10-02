package imagegenlog

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: Printf must redact signed request URLs in both daemon and durable
// diagnostics while preserving failure causes; local log sinks prove both writes.
func TestPrintfRedactsGoogleAPIKeyFromDaemonAndDurableLogs(t *testing.T) {
	dataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("STATE_DIRECTORY", dataHome)

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
	if !strings.Contains(daemonLog.String(), "[REDACTED URL]") || !strings.Contains(daemonLog.String(), "dial tcp failed") {
		t.Fatalf("daemon imagegen log = %q, want redacted key and failure context", daemonLog.String())
	}

	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read durable imagegen log: %v", err)
	}
	if strings.Contains(string(content), secret) {
		t.Fatalf("durable imagegen log leaked google API key:\n%s", string(content))
	}
	if !strings.Contains(string(content), "[REDACTED URL]") || !strings.Contains(string(content), "dial tcp failed") {
		t.Fatalf("durable imagegen log = %q, want redacted key and failure context", string(content))
	}
}

// Purpose: Append must share the privacy sanitizer for header and token echoes;
// reading its isolated durable log proves no credential reaches the sink.
func TestAppendRedactsSecretLikeFields(t *testing.T) {
	dataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("STATE_DIRECTORY", dataHome)

	secret := "secret-token-value"
	Append("[swarmd.imagegen] stage=provider_call_error api_key=\"" + secret + "\" x-goog-api-key:" + secret + " access_token=" + secret)

	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
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
		if !strings.Contains(strings.ToLower(logContent), strings.ToLower(want)) {
			t.Fatalf("imagegen log missing %q after redaction:\n%s", want, logContent)
		}
	}
}
