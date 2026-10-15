package api

import (
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

type sessionsV3ReadLatencyNoopProgressWriter struct{}

func (sessionsV3ReadLatencyNoopProgressWriter) RecordRunPhase(sessionV3ExecutorJob, RunPhase, string) (sessionruntime.SessionMutationResult, error) {
	return sessionruntime.SessionMutationResult{}, nil
}

func (sessionsV3ReadLatencyNoopProgressWriter) RecordRunProgress(sessionV3ExecutorJob, sessionV3AssistantProgress, int) (sessionruntime.SessionMutationResult, error) {
	return sessionruntime.SessionMutationResult{}, nil
}

func (sessionsV3ReadLatencyNoopProgressWriter) RecordReasoningEvent(sessionV3ExecutorJob, string, int, int, string, string, string, string) (sessionruntime.SessionMutationResult, error) {
	return sessionruntime.SessionMutationResult{}, nil
}

func (sessionsV3ReadLatencyNoopProgressWriter) RecordProviderToolConstructionEvent(sessionV3ExecutorJob, string, int, int, provideriface.StreamEvent) (sessionruntime.SessionMutationResult, error) {
	return sessionruntime.SessionMutationResult{}, nil
}
