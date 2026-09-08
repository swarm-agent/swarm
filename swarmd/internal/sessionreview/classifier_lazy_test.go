package sessionreview

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type lazyReviewGit func(...string) (string, error)

func (f lazyReviewGit) Run(_ context.Context, _ string, args ...string) (string, error) {
	return f(args...)
}

// Requirement: CommitMatchesResolvedIntegration reads paths only after a target
// identity match; unmatched commits stay missing and read failures fail closed.
// A strict runner proves command ordering and absence of unnecessary subprocesses
// at the shared classifier used by review and integration preflight.
func TestResolvedIntegrationDefersUnneededReads(t *testing.T) {
	for _, scenario := range []string{"no candidate", "wrong identity", "matching", "path failure", "history failure"} {
		t.Run(scenario, func(t *testing.T) {
			var calls []string
			runner := lazyReviewGit(func(args ...string) (string, error) {
				command := strings.Join(args, " ")
				calls = append(calls, command)
				switch command {
				case "show -s --format=%s source":
					return "subject", nil
				case "log target --format=%H --fixed-strings --grep=subject":
					if scenario == "no candidate" {
						return "", nil
					}
					if scenario == "history failure" {
						return "", errors.New("history unavailable")
					}
					return "candidate", nil
				case "show -s --format=%an%x00%ae%x00%aI%x00%B source":
					return "identity", nil
				case "show -s --format=%an%x00%ae%x00%aI%x00%B candidate":
					if scenario == "wrong identity" {
						return "different", nil
					}
					return "identity", nil
				case "diff-tree --root --no-commit-id --name-only --no-renames -r source":
					if scenario == "path failure" {
						return "", errors.New("paths unavailable")
					}
					return "file.txt", nil
				case "diff-tree --root --no-commit-id --name-only --no-renames -r candidate":
					return "file.txt", nil
				default:
					t.Fatalf("unexpected command %s", command)
					return "", nil
				}
			})
			matched, err := CommitMatchesResolvedIntegration(context.Background(), runner, "repo", "target", "source")
			if matched != (scenario == "matching") {
				t.Fatalf("matched=%v calls=%v", matched, calls)
			}
			wantError := scenario == "path failure" || scenario == "history failure"
			if (err != nil) != wantError {
				t.Fatalf("error=%v", err)
			}
			if (scenario == "no candidate" || scenario == "history failure") && len(calls) != 2 {
				t.Fatalf("unneeded reads: %v", calls)
			}
			if scenario == "wrong identity" && len(calls) != 4 {
				t.Fatalf("unneeded paths: %v", calls)
			}
		})
	}
}
