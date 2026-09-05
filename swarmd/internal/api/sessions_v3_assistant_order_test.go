package api

import (
	"context"
	"errors"
	"testing"

	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
)

// Use the canonical account model authority rather than the session preference:
// otherwise these fixtures fail before the fake provider can stream any text.
func configureAssistantOrderTestProvider(t *testing.T, server *Server) {
	t.Helper()
	assignment := agentmodelsettings.Assignment{Provider: "test-provider", Model: "test-model", Thinking: "medium"}
	if _, err := server.agentModelSettings.ReplaceSwarm(identity.ContextWithPrincipal(context.Background(), testPrincipal()), agentmodelsettings.SwarmInput{
		Action: assignment, Plan: assignment,
	}); err != nil {
		t.Fatalf("configure stream test provider: %v", err)
	}
}

// Requirement: committed assistant messages must retain their first durable text
// event as their timeline anchor. Prevent later flushes, other streams and failed
// writes from replacing it. persistEpoch is the narrow persistence boundary;
// a fake writer keeps the proof deterministic without provider or store timing.
type assistantOrderWriter struct {
	sessionV3DurableProgressWriter
	seq uint64
	err error
}

func (w *assistantOrderWriter) RecordRunProgress(_ sessionV3ExecutorJob, _ sessionV3AssistantProgress, _ int) (sessionruntime.SessionMutationResult, error) {
	return sessionruntime.SessionMutationResult{PrimarySeq: w.seq}, w.err
}

func TestV3AssistantStartSeqRetainsFirstSuccessfulDurableEvent(t *testing.T) {
	writer := &assistantOrderWriter{seq: 10}
	sink := &sessionV3DurableProgressSink{writer: writer, assistantStartSeqByStream: make(map[string]uint64)}
	persist := func(stream string) error {
		return sink.persistEpoch(sessionV3DurableProgressEpoch{Items: []sessionV3DurableProgressItem{{
			Kind:      sessionV3DurableProgressItemAssistant,
			Assistant: &sessionV3AssistantProgressAggregate{StreamID: stream},
		}}})
	}
	if err := persist("stream-a"); err != nil {
		t.Fatal(err)
	}
	writer.seq = 20
	if err := persist("stream-a"); err != nil {
		t.Fatal(err)
	}
	if err := persist("stream-b"); err != nil {
		t.Fatal(err)
	}
	if sink.AssistantStartSeq("stream-a") != 10 || sink.AssistantStartSeq("stream-b") != 20 {
		t.Fatal("stream anchors changed or leaked between streams")
	}
	writer.seq = 30
	writer.err = errors.New("injected persistence failure")
	if err := persist("stream-c"); !errors.Is(err, writer.err) {
		t.Fatalf("expected write failure, got %v", err)
	}
	if sink.AssistantStartSeq("stream-c") != 0 || sink.AssistantFlushCount() != 3 || sink.AssistantStartSeq("stream-a") != 10 {
		t.Fatal("failed persistence published or altered an anchor")
	}
	metadata := (sessionV3AssistantResponse{StreamID: "stream-a", StreamStartSeq: sink.AssistantStartSeq("stream-a")}).metadata("run-a")
	if metadata["stream_start_seq"] != uint64(10) {
		t.Fatalf("committed metadata dropped stream anchor: %#v", metadata)
	}
}
