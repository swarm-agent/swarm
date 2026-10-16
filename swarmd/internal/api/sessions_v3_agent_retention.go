package api

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Conversation retention for sealed agents: an agent whose limits set
// retention_days has its ended or idle conversations hard deleted (the same
// delete as DELETE /v3/sessions/{id}) once that many days have passed since
// their last activity. The default (0) keeps conversations until someone
// deletes them; at most pebblestore.AgentRetentionMaxDays. Only sealed agents
// are swept, so a retention setting can never delete a coding session.
//
// A pass deletes at most agentRetentionBatch sessions, skips a session with a
// run in flight or a pending tool call (the next pass retries it), deletes
// only if the session is unchanged since it was chosen, logs what it did and
// raises a session.retention.failed signal for an agent whose deletes failed.

const (
	agentRetentionBatch     = 100
	agentRetentionScanLimit = 100_000
)

// AgentRetentionResult reports one sweep pass.
type AgentRetentionResult struct {
	Deleted int
	Skipped int
	Failed  int
	// More is true when eligible sessions remained beyond this pass's batch.
	More bool
}

type agentRetentionKey struct{ account, agent string }

// agentRetentionDays returns the retention for a sealed agent, 0 for none.
func (s *Server) agentRetentionDays(account, agent string) (int, error) {
	if s.agents == nil || agent == "" {
		return 0, nil
	}
	profile, ok, err := s.agents.GetProfileForAccount(account, agent)
	if err != nil {
		return 0, err
	}
	if !ok || profile.Limits == nil || profile.Limits.RetentionDays <= 0 {
		return 0, nil
	}
	if s.sealedAgentError(account, agent) != nil {
		return 0, nil
	}
	return min(profile.Limits.RetentionDays, pebblestore.AgentRetentionMaxDays), nil
}

// SweepAgentSessionRetention runs one bounded retention pass at now.
func (s *Server) SweepAgentSessionRetention(ctx context.Context, now time.Time) (AgentRetentionResult, error) {
	var result AgentRetentionResult
	if s == nil || s.sessions == nil || s.agents == nil {
		return result, nil
	}
	days := map[agentRetentionKey]int{}
	var lookupErr error
	// The filter sees every stored session, so only past-retention ones are
	// kept and the oldest are never cut off by the listing limit.
	expired := func(session pebblestore.SessionSnapshot) bool {
		agent := sessionsV3MetadataString(session.Metadata, "agent_name")
		if agent == "" || session.AccountScopeID == "" || lookupErr != nil {
			return false
		}
		key := agentRetentionKey{session.AccountScopeID, agent}
		retention, known := days[key]
		if !known {
			var err error
			if retention, err = s.agentRetentionDays(key.account, key.agent); err != nil {
				lookupErr = fmt.Errorf("read retention of agent %q: %w", agent, err)
				return false
			}
			days[key] = retention
		}
		if retention <= 0 {
			return false
		}
		cutoff := now.Add(-time.Duration(retention) * 24 * time.Hour).UnixMilli()
		endedAt := sessionEndedAt(session)
		return session.UpdatedAt < cutoff || (endedAt > 0 && endedAt < cutoff)
	}
	all, err := s.sessions.ListSessionsMatching(agentRetentionScanLimit, expired)
	if err != nil {
		return result, fmt.Errorf("list sessions for retention: %w", err)
	}
	if lookupErr != nil {
		return result, lookupErr
	}
	type candidate struct {
		session pebblestore.SessionSnapshot
		key     agentRetentionKey
		last    int64
	}
	candidates := make([]candidate, 0, len(all))
	for _, session := range all {
		last := session.UpdatedAt
		if endedAt := sessionEndedAt(session); endedAt > 0 && endedAt < last {
			last = endedAt
		}
		candidates = append(candidates, candidate{session: session, key: agentRetentionKey{session.AccountScopeID, sessionsV3MetadataString(session.Metadata, "agent_name")}, last: last})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].last < candidates[j].last })
	if len(candidates) > agentRetentionBatch {
		result.More = true
		candidates = candidates[:agentRetentionBatch]
	}
	deleted := map[agentRetentionKey]int{}
	failed := map[agentRetentionKey][]string{}
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		id := c.session.ID
		if _, active, err := s.sessions.GetSessionActiveRunIntent(id); err != nil {
			failed[c.key] = append(failed[c.key], err.Error())
			continue
		} else if active {
			result.Skipped++
			continue
		}
		if s.perm != nil {
			if pending, err := s.perm.PendingCount(id); err != nil {
				failed[c.key] = append(failed[c.key], err.Error())
				continue
			} else if pending > 0 {
				result.Skipped++
				continue
			}
		}
		events, err := s.sessions.DeleteSessionsWithEventsIfUnchanged([]string{id}, map[string]int64{id: c.session.UpdatedAt})
		if err != nil {
			if strings.Contains(err.Error(), "changed after mutation preview") {
				// Activity since it was chosen: it is no longer past retention.
				result.Skipped++
				continue
			}
			failed[c.key] = append(failed[c.key], err.Error())
			continue
		}
		var event *pebblestore.EventEnvelope
		if len(events) > 0 {
			event = events[0]
		}
		s.publishSessionV3Deleted(id, event)
		deleted[c.key]++
		result.Deleted++
	}
	for key, count := range deleted {
		log.Printf("swarmd retention: deleted %d conversation(s) of agent %q past %d day(s)", count, key.agent, days[key])
	}
	var failures []string
	for key, errs := range failed {
		result.Failed += len(errs)
		log.Printf("warning: swarmd retention: %d conversation(s) of agent %q could not be deleted: %s", len(errs), key.agent, errs[0])
		failures = append(failures, key.agent)
		s.signalAgentRetentionFailed(key, len(errs))
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return result, fmt.Errorf("retention deletes failed for agent(s) %s", strings.Join(failures, ", "))
	}
	return result, nil
}

// signalAgentRetentionFailed raises one warning per agent and pass whose
// retention deletes failed. It names the agent and a count, never content.
func (s *Server) signalAgentRetentionFailed(key agentRetentionKey, count int) {
	if s.signals == nil {
		return
	}
	if _, err := s.signals.Append(pebblestore.Signal{
		Kind: "session.retention.failed", Severity: pebblestore.SignalSeverityWarning, Source: pebblestore.SignalSourceSwarmd,
		Account: key.account, Summary: fmt.Sprintf("Agent %s: %d conversation(s) past retention could not be deleted", key.agent, count),
		DedupKey: "session.retention.failed:" + key.agent, Attrs: map[string]string{"agent": key.agent, "count": strconv.Itoa(count)},
	}); err != nil {
		log.Printf("swarmd signal session.retention.failed not recorded: %v", err)
	}
}
