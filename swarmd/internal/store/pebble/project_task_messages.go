package pebblestore

import (
	"sort"
	"strings"
)

// A task's messages are what was sent to it after it started: each
// message_task note queued into one of its runs, with whether that run's
// model received it (the run loop's feedback delivery receipt), and each
// follow-up that reopened it. They are read from the task's own sessions and
// attempts; nothing is stored for them.
const (
	// ProjectTaskMessageSourceMetadata marks a queued note with who queued
	// it; only the runtime writes it.
	ProjectTaskMessageSourceMetadata = "queued_note_source"
	// ProjectTaskMessageSourceTask is a note queued by message_task.
	ProjectTaskMessageSourceTask = "message_task"

	ProjectTaskMessageQueued    = "queued"
	ProjectTaskMessageDelivered = "delivered"
	ProjectTaskMessageReopened  = "reopened"

	// ProjectTaskMessagesShown is how many messages a task view shows, the
	// newest; ProjectTaskMessageTextRunes is how much of each.
	ProjectTaskMessagesShown    = 3
	ProjectTaskMessageTextRunes = 80
	// projectTaskMessageSessionTail bounds the messages read per session.
	projectTaskMessageSessionTail = 200
)

// ProjectTaskMessage is one message sent to a task: its first
// ProjectTaskMessageTextRunes, its state (queued, delivered, reopened) and
// the run it was queued into, delivered in or reopened as.
type ProjectTaskMessage struct {
	Text  string `json:"text"`
	State string `json:"state"`
	RunID string `json:"run_id,omitempty"`
	At    int64  `json:"at,omitempty"`
}

// ProjectTaskMessages returns the newest ProjectTaskMessagesShown messages
// sent to task, oldest first. The task's sessions must be the account's (the
// caller read the task under the account).
func (s *SessionStore) ProjectTaskMessages(task *ProjectTaskRecord) ([]ProjectTaskMessage, error) {
	out, _, err := s.projectTaskMessages(task, func(sessionID string) ([]MessageSnapshot, bool, error) {
		messages, err := s.ListV3SessionMessageTail(sessionID, projectTaskMessageSessionTail)
		return messages, false, err
	})
	if len(out) > ProjectTaskMessagesShown {
		out = out[len(out)-ProjectTaskMessagesShown:]
	}
	return out, err
}

// projectTaskMessagesPage is the page size of ProjectTaskMessagesAll.
const projectTaskMessagesPage = 500

// ProjectTaskMessagesAll returns every message sent to task, oldest first:
// not the view's newest ProjectTaskMessagesShown, nor a session's message
// tail. Each session's messages are read in pages up to maxPerSession (all
// when 0); truncated reports that a session had more.
func (s *SessionStore) ProjectTaskMessagesAll(task *ProjectTaskRecord, maxPerSession int) (messages []ProjectTaskMessage, truncated bool, err error) {
	return s.projectTaskMessages(task, func(sessionID string) ([]MessageSnapshot, bool, error) {
		var out []MessageSnapshot
		after := uint64(0)
		for {
			page, err := s.ListV3SessionMessages(sessionID, after, projectTaskMessagesPage)
			if err != nil {
				return nil, false, err
			}
			for _, m := range page {
				if maxPerSession > 0 && len(out) >= maxPerSession {
					return out, true, nil
				}
				out = append(out, m)
				after = m.GlobalSeq
			}
			if len(page) < projectTaskMessagesPage {
				return out, false, nil
			}
		}
	})
}

// projectTaskMessages collects the messages sent to task from the sessions'
// messages that read returns, oldest first.
func (s *SessionStore) projectTaskMessages(task *ProjectTaskRecord, read func(sessionID string) ([]MessageSnapshot, bool, error)) ([]ProjectTaskMessage, bool, error) {
	if s == nil || task == nil {
		return nil, false, nil
	}
	var out []ProjectTaskMessage
	truncated := false
	seen := map[string]bool{}
	sessions := []string{task.SessionID}
	for i, attempt := range task.Attempts {
		sessions = append(sessions, attempt.SessionID)
		// Every attempt after the first is a follow-up that reopened the
		// task with its request (message_task or reopen_task).
		if i > 0 && strings.TrimSpace(attempt.Request) != "" {
			out = append(out, ProjectTaskMessage{Text: clipProjectTaskMessage(attempt.Request), State: ProjectTaskMessageReopened, RunID: attempt.RunID, At: attempt.CreatedAt})
		}
	}
	for _, sessionID := range sessions {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" || seen[sessionID] {
			continue
		}
		seen[sessionID] = true
		messages, more, err := read(sessionID)
		if err != nil {
			return nil, false, err
		}
		truncated = truncated || more
		delivered := map[string]string{}
		for _, m := range messages {
			if m.Role == "system" && m.Metadata["source"] == "feedback_delivery" {
				id, _ := m.Metadata["message_id"].(string)
				run, _ := m.Metadata["run_id"].(string)
				if id != "" && delivered[id] == "" {
					delivered[id] = run
				}
			}
		}
		for _, m := range messages {
			if m.Role != "user" || m.Metadata["feedback_note"] != true || m.Metadata[ProjectTaskMessageSourceMetadata] != ProjectTaskMessageSourceTask {
				continue
			}
			msg := ProjectTaskMessage{Text: clipProjectTaskMessage(m.Content), State: ProjectTaskMessageQueued, At: m.CreatedAt}
			msg.RunID, _ = m.Metadata["queued_for_run_id"].(string)
			if run, ok := delivered[m.ID]; ok {
				msg.State = ProjectTaskMessageDelivered
				if run != "" {
					msg.RunID = run
				}
			}
			out = append(out, msg)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out, truncated, nil
}

func clipProjectTaskMessage(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > ProjectTaskMessageTextRunes {
		return string(r[:ProjectTaskMessageTextRunes])
	}
	return text
}
