package api

import (
	"sync"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
)

type sessionsV3DurableProgressRecordingWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once

	mu        sync.Mutex
	assistant []sessionV3AssistantProgress
	phases    []string
	reasoning []string
	provider  []sessionsV3DurableProgressProviderRecord
}

type sessionsV3DurableProgressProviderRecord struct {
	eventType  string
	eventIndex int
	event      provideriface.StreamEvent
}

func (w *sessionsV3DurableProgressRecordingWriter) maybeBlock() {
	if w == nil || w.entered == nil {
		return
	}
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
}

func (w *sessionsV3DurableProgressRecordingWriter) RecordRunPhase(job sessionV3ExecutorJob, phase RunPhase, eventType string) (sessionruntime.SessionMutationResult, error) {
	w.maybeBlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.phases = append(w.phases, eventType)
	return sessionruntime.SessionMutationResult{}, nil
}

func (w *sessionsV3DurableProgressRecordingWriter) RecordRunProgress(job sessionV3ExecutorJob, progress sessionV3AssistantProgress, deltaIndex int) (sessionruntime.SessionMutationResult, error) {
	w.maybeBlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.assistant = append(w.assistant, progress)
	return sessionruntime.SessionMutationResult{}, nil
}

func (w *sessionsV3DurableProgressRecordingWriter) RecordReasoningEvent(job sessionV3ExecutorJob, eventType string, step int, eventIndex int, reasoningKey string, delta string, deltaMode string, summary string) (sessionruntime.SessionMutationResult, error) {
	w.maybeBlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reasoning = append(w.reasoning, eventType+":"+delta+":"+deltaMode+":"+summary)
	return sessionruntime.SessionMutationResult{}, nil
}

func (w *sessionsV3DurableProgressRecordingWriter) RecordProviderToolConstructionEvent(job sessionV3ExecutorJob, eventType string, step int, eventIndex int, event provideriface.StreamEvent) (sessionruntime.SessionMutationResult, error) {
	w.maybeBlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.provider = append(w.provider, sessionsV3DurableProgressProviderRecord{eventType: eventType, eventIndex: eventIndex, event: event})
	return sessionruntime.SessionMutationResult{}, nil
}
