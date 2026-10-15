package api

import (
	"log"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// signalSessionRunEnded records session.run.failed or session.run.interrupted
// (warnings) when a session run newly ends without completing, so a monitor on
// the signal feed hears about a broken chat (a public sealed agent included)
// without polling sessions. It names the session, run and agent; the failure
// reason and the conversation stay on the machine. A replayed or refused status
// write raises nothing.
func (s *Server) signalSessionRunEnded(job sessionV3ExecutorJob, status string, result sessionruntime.SessionMutationResult) {
	if s == nil || s.signals == nil || result.Replayed || result.Conflict != nil || result.Error != nil {
		return
	}
	var kind, ended string
	switch status {
	case sessionruntime.RunIntentFailed:
		kind, ended = "session.run.failed", "failed"
	case sessionruntime.RunIntentInterrupted:
		kind, ended = "session.run.interrupted", "was interrupted"
	default:
		return
	}
	refs := map[string]string{"session_id": job.SessionID, "run_id": job.RunID}
	attrs := map[string]string{}
	summary := "A session run " + ended
	if s.sessions != nil {
		if session, ok, err := s.sessions.GetSession(job.SessionID); err == nil && ok {
			if agent := sessionsV3MetadataString(session.Metadata, "agent_name"); agent != "" {
				attrs["agent"] = agent
				summary = "Agent " + agent + " run " + ended
			}
		}
	}
	if _, err := s.signals.Append(pebblestore.Signal{
		Kind: kind, Severity: pebblestore.SignalSeverityWarning, Source: pebblestore.SignalSourceSwarmd,
		Account: job.Principal.AccountScopeID, Summary: summary, DedupKey: kind + ":" + job.RunID,
		Refs: refs, Attrs: attrs,
	}); err != nil {
		log.Printf("swarmd signal %s not recorded: %v", kind, err)
	}
}
