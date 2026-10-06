package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// BuildCommandError exposes only trusted structure and fixed hints, never recipe
// output, argv, paths or the underlying error text. ExitCode is the observed
// launcher status (systemd-run for build), not necessarily an engine status.
// Only cancellation sentinels are unwrapped: underlying errors may hold secrets.
type BuildCommandError struct {
	Phase string
	Kind  string
	Code  int
	hint  string
	cause error
}

func (e *BuildCommandError) Error() string {
	return fmt.Sprintf("isolated image %s failed: kind=%s exit_code=%d; %s; no image accepted", e.Phase, e.Kind, e.Code, e.hint)
}
func (e *BuildCommandError) ExitCode() int { return e.Code }

func (e *BuildCommandError) Unwrap() error { return e.cause }

// runBuildCommand drains output without retaining more than 16 KiB of stderr.
// Recipe output can impersonate engine errors, so matches are troubleshooting
// hints only, never authority for admission, retries or security policy changes.
func (p *LocalDockerProvider) runBuildCommand(ctx context.Context, phase string, stdout io.Writer, name string, args ...string) error {
	var stderr bytes.Buffer
	capture := &boundedBuffer{buf: &stderr, max: 16 * 1024}
	err := p.runner.RunWithIO(ctx, nil, stdout, capture, name, args...)
	if err == nil && ctx.Err() == nil {
		return nil
	}
	failure := &BuildCommandError{Phase: phase, Kind: "runner", Code: 1, hint: "output withheld; inspect private operator diagnostics"}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		failure.Kind, failure.Code, failure.cause = "deadline", 124, context.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		failure.Kind, failure.Code, failure.cause = "cancelled", 130, context.Canceled
	default:
		var exit interface{ ExitCode() int }
		if errors.As(err, &exit) {
			failure.Kind, failure.Code = "exit", exit.ExitCode()
			if failure.Code < 0 {
				failure.Kind = "signal"
			}
		} else if errors.Is(err, exec.ErrNotFound) {
			failure.Kind, failure.Code = "executable-unavailable", 127
		}
		failure.hint = buildInfrastructureHint(stderr.String())
	}
	return failure
}

func buildInfrastructureHint(output string) string {
	output = strings.ToLower(output)
	// Never interpolate even part of a matching line: it may contain arbitrary
	// source secrets, credentials, terminal escapes, or forged engine messages.
	for _, candidate := range []struct{ match, hint string }{
		{"failed to connect to bus", "check the configured user session bus"},
		{"no space left on device", "check build storage capacity and inode availability"},
		{"cannot find a working conmon", "check the installed conmon runtime"},
		{"crun", "check the installed crun runtime and delegated cgroup configuration"},
		{"slirp4netns", "check the installed rootless network helper"},
		{"newuidmap", "check rootless UID mapping helper and subordinate ID configuration"},
		{"newgidmap", "check rootless GID mapping helper and subordinate ID configuration"},
		{"cgroup", "check user-service cgroup delegation and resource limits"},
		{"short-name", "use a fully qualified base image name in the committed recipe"},
		{"tls handshake", "check registry connectivity and certificate trust"},
		{"runroot", "check engine runroot path length and runtime directory configuration"},
	} {
		if strings.Contains(output, candidate.match) {
			return "untrusted output hint: " + candidate.hint
		}
	}
	return "output withheld; inspect private operator diagnostics"
}
