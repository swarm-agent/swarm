package api

import (
	"encoding/json"
	"strings"

	"swarm/packages/swarmd/internal/jobautomation"
)

// automationHandoff reads the existing task_progress completion summary, not a
// second reporting tool. The job report remains the runner's fallback.
func (s *Server) automationHandoff(sessionID string) string {
	if sessionID == "" || s.sessions == nil {
		return ""
	}
	var after uint64
	summary := ""
	for page := 0; page < reviewMaxMessagePages; page++ {
		messages, err := s.sessions.ListMessages(sessionID, after, reviewMessagesPageSize)
		if err != nil {
			return summary
		}
		for _, m := range messages {
			after = m.GlobalSeq
			if m.Role != "tool" {
				continue
			}
			var rec struct {
				ToolName  string `json:"tool_name"`
				Arguments string `json:"arguments"`
				Error     string `json:"error"`
			}
			if json.Unmarshal([]byte(m.Content), &rec) != nil || controlMCPToolName(rec.ToolName) != "task_progress" || rec.Error != "" {
				continue
			}
			var args struct {
				Action  string `json:"action"`
				Summary string `json:"summary"`
			}
			if json.Unmarshal([]byte(rec.Arguments), &args) == nil && (args.Action == "done" || args.Action == "complete") && strings.TrimSpace(args.Summary) != "" {
				summary = controlMCPTruncate(args.Summary, jobautomation.MaxHandoffBytes)
			}
		}
		if len(messages) < reviewMessagesPageSize {
			break
		}
	}
	return summary
}
