package api

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3InjectCheckpointResumeRoutingMessageImmediatelyBeforeUserMessage(t *testing.T) {
	user := pebblestore.MessageSnapshot{SessionID: "session-1", Role: "user", Content: "ok use that", CreatedAt: 42}
	markSessionsV3CheckpointResumeRouting(&user, "run-2", "followup-5", "resume_paused_user_message")
	messages := []pebblestore.MessageSnapshot{
		{SessionID: "session-1", Role: "assistant", Content: "Use 15 uncommitted."},
		user,
	}

	got := sessionsV3InjectCheckpointResumeRoutingMessage(messages, "run-2")
	if len(got) != 3 || got[1].Role != "system" || got[2].Content != "ok use that" {
		t.Fatalf("injected messages = %#v", got)
	}
	for _, want := range []string{
		"Active checkpoint resumed by this user message",
		"continues checkpoint followup-5",
		"Do not call start_session_checkpoint or transition_checkpoint_boundary", "request_followup_checkpoint and its aliases are retired",
		"continue without plan mutation",
		"use add_subtask",
		"use replace_subtasks",
		"use restart_checkpoint",
		"Preserve unrelated later checkpoints",
	} {
		if !strings.Contains(got[1].Content, want) {
			t.Fatalf("routing message missing %q:\n%s", want, got[1].Content)
		}
	}
	if got[1].Metadata["source"] != "checkpoint_resume_routing" || got[1].Metadata["checkpoint_id"] != "followup-5" || got[1].Metadata["synthetic"] != true {
		t.Fatalf("routing metadata = %#v", got[1].Metadata)
	}
}

func TestSessionsV3InjectCheckpointResumeRoutingMessageIgnoresOrdinaryUserMessage(t *testing.T) {
	messages := []pebblestore.MessageSnapshot{{SessionID: "session-1", Role: "user", Content: "ordinary message"}}
	got := sessionsV3InjectCheckpointResumeRoutingMessage(messages, "run-2")
	if len(got) != 1 || got[0].Content != "ordinary message" {
		t.Fatalf("ordinary messages changed = %#v", got)
	}
}

func TestSessionsV3InjectCheckpointResumeRoutingMessageRequiresMatchingRun(t *testing.T) {
	user := pebblestore.MessageSnapshot{SessionID: "session-1", Role: "user", Content: "continue"}
	markSessionsV3CheckpointResumeRouting(&user, "run-1", "cp-1", "resume_paused_user_message")
	got := sessionsV3InjectCheckpointResumeRoutingMessage([]pebblestore.MessageSnapshot{user}, "run-2")
	if len(got) != 1 || got[0].Role != "user" {
		t.Fatalf("stale resume routing marker was injected = %#v", got)
	}
}
