package api

import (
	"errors"
	"log"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var errSessionV3TaskWaitYield = errors.New("provider run durably yielded for project tasks")

func (e *sessionV3Executor) taskWaitYielded(job sessionV3ExecutorJob) bool {
	if e == nil || e.server == nil || e.server.sessions == nil {
		return false
	}
	intent, ok, err := e.server.sessions.GetSessionRunIntent(job.SessionID, job.RunID)
	return err == nil && ok && intent.TaskWait != nil && (intent.Status == pebblestore.V3RunIntentWaitingTasks || intent.Status == pebblestore.V3RunIntentCompleted)
}

func (s *Server) reconcileProjectTaskWaits(account, project string) {
	if s == nil || s.sessions == nil {
		return
	}
	err := s.sessions.Store().ReconcileProjectTaskWaits(account, project, func(result pebblestore.V3SessionMutationResult) {
		if result.RealtimeOutbox != nil {
			if err := s.publishCommittedV3RealtimeOutbox(*result.RealtimeOutbox); err != nil {
				log.Print("task wait outbox publication failed; durable recovery retained")
			}
		}
		if e := s.v3SessionExecutor; e != nil && result.RunIntent != nil {
			intent := result.RunIntent
			e.EnqueueRun(sessionV3ExecutorJob{SessionID: intent.SessionID, RunID: intent.RunID, SourceMessageID: intent.SourceMessageID, EpochID: intent.EpochID, ResumeContext: true, Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: intent.UserID, AccountScopeID: intent.AccountScopeID}})
		}
	})
	if err != nil {
		log.Printf("project task wait reconciliation failed: %v", err)
	}
}

// Runtime tools commit through the session service. Publish that exact retained
// outbox record on yield as well, so connected clients need not reconnect to see
// inactive waiting state. Durable replay remains authoritative.
func (e *sessionV3Executor) publishTaskWaitState(job sessionV3ExecutorJob) {
	intent, found, err := e.server.sessions.GetSessionRunIntent(job.SessionID, job.RunID)
	if err != nil || !found || intent.Status != pebblestore.V3RunIntentWaitingTasks || intent.EventSeq == 0 {
		return
	}
	records, err := e.server.sessions.Store().ListV3RealtimeOutboxForSessionAfterSeq(job.SessionID, intent.EventSeq-1, 1)
	if err != nil {
		log.Printf("task wait publication read failed: %v", err)
		return
	}
	if len(records) == 1 && records[0].Event.Seq == intent.EventSeq {
		if err := e.server.publishCommittedV3RealtimeOutbox(records[0]); err != nil {
			log.Printf("task wait publication failed: %v", err)
		}
	}
}
