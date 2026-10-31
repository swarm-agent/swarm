package claudecode

import (
	"errors"
	"strings"
	"swarm/packages/swarmd/internal/session"
)

func (r *Runner) SetSessionMCP(mcp *SessionMCP, sessions *session.Service) {
	r.mcp = mcp
	r.sessions = sessions
}

var errCLIResumeUnavailable = errors.New("Claude Code session cannot be resumed")

func resumeRefused(detail string) bool {
	detail = strings.ToLower(detail)
	return strings.Contains(detail, "no conversation found with session id") || strings.Contains(detail, "session not found") || strings.Contains(detail, "failed to resume session")
}

// New user messages follow the last assistant turn in the durable input. Do not
// send the already persisted CLI conversation again on a successful resume.
func newUserInput(input []map[string]any) []map[string]any {
	start := 0
	for i, item := range input {
		if stringField(item, "role") == "assistant" {
			start = i + 1
		}
	}
	var result []map[string]any
	for _, item := range input[start:] {
		if stringField(item, "role") == "user" {
			result = append(result, item)
		}
	}
	return result
}
