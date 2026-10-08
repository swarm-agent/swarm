package pebblestore

import "errors"

// Session messaging is not a task reporting channel, including trigger:false.
// Ordinary user chat and Orchestrator-to-task feedback remain unchanged.
func (s *SessionStore) guardProjectTaskReportBypass(input V3SessionMutationInput) error {
	if input.Message == nil {
		return nil
	}
	senderID, _ := input.Message.Metadata["sender_session_id"].(string)
	if senderID == "" {
		return nil
	}
	target, found, err := s.GetSession(input.SessionID)
	if err != nil {
		return err
	}
	if !found || ProjectConversationID(target) == "" {
		return nil
	}
	sender, found, err := s.GetSession(senderID)
	if err != nil {
		return err
	}
	if !found || sender.AccountScopeID != input.AccountScopeID || sender.UserID != input.UserID {
		return errors.New("session message sender ownership mismatch")
	}
	if sender.Metadata["task_id"] != nil || sender.Metadata["project_task_id"] != nil {
		return errors.New("task-to-Orchestrator messages must use manage_projects report_task, not session messaging")
	}
	return nil
}
