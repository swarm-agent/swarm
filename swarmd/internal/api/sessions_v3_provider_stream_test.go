package api

import (
	"sync"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
)

type sessionsV3BlockingDurableProgressWriter struct {
	base    sessionV3DurableProgressWriter
	entered chan struct{}
	release chan struct{}
	once    sync.Once

	mu             sync.Mutex
	assistantCalls []sessionV3AssistantProgress
	phaseCalls     []string
}

func (w *sessionsV3BlockingDurableProgressWriter) blockFirst() {
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
}

func (w *sessionsV3BlockingDurableProgressWriter) RecordRunPhase(job sessionV3ExecutorJob, phase RunPhase, eventType string) (sessionruntime.SessionMutationResult, error) {
	w.blockFirst()
	w.mu.Lock()
	w.phaseCalls = append(w.phaseCalls, eventType)
	w.mu.Unlock()
	if w.base == nil {
		return sessionruntime.SessionMutationResult{}, nil
	}
	return w.base.RecordRunPhase(job, phase, eventType)
}

func (w *sessionsV3BlockingDurableProgressWriter) RecordRunProgress(job sessionV3ExecutorJob, progress sessionV3AssistantProgress, deltaIndex int) (sessionruntime.SessionMutationResult, error) {
	w.blockFirst()
	w.mu.Lock()
	w.assistantCalls = append(w.assistantCalls, progress)
	w.mu.Unlock()
	if w.base == nil {
		return sessionruntime.SessionMutationResult{}, nil
	}
	return w.base.RecordRunProgress(job, progress, deltaIndex)
}

func (w *sessionsV3BlockingDurableProgressWriter) RecordReasoningEvent(job sessionV3ExecutorJob, eventType string, step int, eventIndex int, reasoningKey string, delta string, deltaMode string, summary string) (sessionruntime.SessionMutationResult, error) {
	w.blockFirst()
	if w.base == nil {
		return sessionruntime.SessionMutationResult{}, nil
	}
	return w.base.RecordReasoningEvent(job, eventType, step, eventIndex, reasoningKey, delta, deltaMode, summary)
}

func (w *sessionsV3BlockingDurableProgressWriter) RecordProviderToolConstructionEvent(job sessionV3ExecutorJob, eventType string, step int, eventIndex int, event provideriface.StreamEvent) (sessionruntime.SessionMutationResult, error) {
	w.blockFirst()
	if w.base == nil {
		return sessionruntime.SessionMutationResult{}, nil
	}
	return w.base.RecordProviderToolConstructionEvent(job, eventType, step, eventIndex, event)
}
