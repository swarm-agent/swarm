package workspace

import (
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/gitenv"
)

// A repository command's environment drops the daemon's GIT_* variables but
// keeps its own sharing override: a person's folder set to
// core.sharedRepository=group must not make Git in the daemon set setgid on
// the directories it creates (RestrictSUIDSGID refuses that, and the first
// commit there failed).
func TestRepositoryEnvironmentKeepsDaemonSharingOverride(t *testing.T) {
	t.Setenv("GIT_DIR", "/elsewhere")
	if err := gitenv.UseUmaskSharing(); err != nil {
		t.Fatal(err)
	}
	env := repositoryEnvironment([]string{"GIT_INDEX_FILE=/tmp/index"})
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range []string{"\nGIT_CONFIG_KEY_0=core.sharedrepository\n", "\nGIT_CONFIG_VALUE_0=umask\n", "\nGIT_CONFIG_COUNT=1\n", "\nGIT_INDEX_FILE=/tmp/index\n"} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment lacks %q:\n%s", strings.TrimSpace(want), joined)
		}
	}
	if strings.Contains(joined, "\nGIT_DIR=") {
		t.Errorf("inherited GIT_DIR reached the repository command:\n%s", joined)
	}
}
