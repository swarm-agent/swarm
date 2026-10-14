package sandbox

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/environments/provider"
)

// Purpose: Manager.Exec delegates to ExecSupervised, whose output boundary must
// preserve Git evidence while redacting credentials, not non-secret routing env.
// This fake transport exercises the real supervisor result without Docker.
func TestExecGitOutputRedaction(t *testing.T) {
	head := "a1" + strings.Repeat("b", 38)
	res, err := provider.ExecSupervised(context.Background(), &outputTransport{head + "\n"}, "box", provider.ExecRequest{Command: []string{"git", "rev-parse", "HEAD"}, Env: map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "safe.directory", "GIT_CONFIG_VALUE_0": "/project"}, Timeout: time.Second}, time.Second)
	if err != nil || res.Stdout != head+"\n" || res.ExitCode != 0 {
		t.Fatalf("Git evidence corrupted: %+v %v", res, err)
	}
	for _, tc := range []struct {
		key, value string
		redact     bool
	}{
		{"API_TOKEN", "fixture-secret-value", true},
		{"CUSTOM", "swarm-secret://fixture", true},
		{"API_TOKEN", "1", false},
		{"HOME", "/project", false},
		{"PATH", "/usr/bin:/bin", false},
	} {
		out, _ := provider.SafeExecOutput(tc.value+"\n", 4096, map[string]string{tc.key: tc.value})
		if strings.Contains(out, tc.value) == tc.redact {
			t.Errorf("%s redaction mismatch: %q", tc.key, out)
		}
	}
}

type outputTransport struct{ output string }

func (f *outputTransport) RunExec(_ context.Context, _ io.Reader, stdout, _ io.Writer, _ ...string) error {
	_, err := io.WriteString(stdout, f.output)
	return err
}
func (f *outputTransport) RunExecCombined(context.Context, ...string) ([]byte, error) {
	return nil, nil
}
