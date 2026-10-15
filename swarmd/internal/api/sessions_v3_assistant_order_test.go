package api

import (
	sessionruntime "swarm/packages/swarmd/internal/session"
)

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
