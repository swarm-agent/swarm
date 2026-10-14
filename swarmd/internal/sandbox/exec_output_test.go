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
// preserve Git evidence while default-redacting long env values except the
// explicit non-secret routing keys. Caller-marked secrets override exemptions.
// This fake transport exercises the real supervisor result without Docker.
func TestExecGitOutputRedaction(t *testing.T) {
	head := "a1" + strings.Repeat("b", 38)
	res, err := provider.ExecSupervised(context.Background(), &outputTransport{head + "\n"}, "box", provider.ExecRequest{Command: []string{"git", "rev-parse", "HEAD"}, Env: map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "safe.directory", "GIT_CONFIG_VALUE_0": "/project", "CUSTOM": "a1", "API_TOKEN": "bbbbbbb"}, Timeout: time.Second}, time.Second)
	if err != nil || res.Stdout != head+"\n" || res.ExitCode != 0 {
		t.Fatalf("Git evidence corrupted: %+v %v", res, err)
	}
	marked, _ := provider.SafeExecOutput("fixture-marked-secret\n", 4096, nil, "fixture-marked-secret")
	if strings.Contains(marked, "fixture-marked-secret") {
		t.Fatal("caller-marked secret leaked")
	}
	markedExempt, _ := provider.SafeExecOutput("/project/cache\n", 4096, map[string]string{"GIT_CONFIG_VALUE_0": "/project/cache"}, "/project/cache")
	if strings.Contains(markedExempt, "/project/cache") {
		t.Fatal("caller-marked exempt value leaked")
	}
	short, _ := provider.SafeExecOutput("1\n", 4096, nil, "1")
	if short != "1\n" {
		t.Fatal("short marked value corrupted output")
	}
	for _, tc := range []struct {
		key, value string
		redact     bool
	}{
		{"API_TOKEN", "fixture-secret-value", true},
		{"DATABASE_URL", "postgres://user:pass@database/app", true},
		{"MY_APP_CONFIG", "fixture-config-value", true},
		{"CUSTOM", "12345678", true},
		{"CUSTOM", "1234567", false},
		{"GIT_CONFIG_VALUE_0", "/project/cache", false},
		{"GIT_CONFIG_OTHER", "fixture-private-value", true},
		{"CUSTOM_UMASK", "fixture-private-value", true},
		{"CUSTOM", "swarm-secret://fixture", true},
		{"API_TOKEN", "1", false},
		{"HOME", "/project", false},
		{"PATH", "/usr/bin:/bin", false},
		{"GIT_CONFIG_COUNT", "12345678", false},
		{"GIT_CONFIG_KEY_0", "safe.directory", false},
		{"GIT_DIR", "/project/.git", false},
		{"GIT_WORK_TREE", "/project/work", false},
		{"GIT_INDEX_FILE", "/project/index", false},
		{"UMASK", "00000007", false},
		{"GOCACHE", "/project/cache", false},
		{"GOMODCACHE", "/project/modules", false},
		{"GOPATH", "/project/go", false},
		{"npm_config_store_dir", "/project/pnpm", false},
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
