package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Box summary bounds: the summary is one cheap call a monitor makes per
// machine, so it lists at most this many blocked sessions and workers.
const (
	boxSummaryMaxBlocked = 20
	boxSummaryMaxWorkers = 200
	boxSummaryMaxListed  = 20
)

// Box status values, from best to worst.
const (
	BoxStatusOK        = "ok"
	BoxStatusAttention = "attention" // something waits on a person or failed today
	BoxStatusDegraded  = "degraded"  // agents are not confined, or spending is over the limit
)

// SetSecretsGatewayEnabled records whether the agent secret gateway runs, for
// the box summary.
func (s *Server) SetSecretsGatewayEnabled(enabled bool) {
	if s != nil {
		s.secretsGatewayEnabled = enabled
	}
}

type boxBlockedSession struct {
	SessionID       string `json:"session_id"`
	Pending         int    `json:"pending"`
	OldestPendingAt int64  `json:"oldest_pending_at"`
}

type boxWorker struct {
	WorkerID    string `json:"worker_id"`
	Name        string `json:"name"`
	TodayRuns   int    `json:"today_runs"`
	TodayFailed int    `json:"today_failed"`
	ActiveRuns  int    `json:"active_runs"`
}

// handleBoxSummary answers "is this machine all right, and does anything need
// me?" in one call (GET /v3/box/summary, scope signals:read): sandbox and
// secret-gateway state, sessions waiting on a person, workers that failed
// today, usage against the daily limit, and the signal feed position. It
// holds ids, names and counts only. Status is "degraded" when agents run
// unconfined or spending is over the limit, "attention" when something waits
// on a person or a worker failed today, else "ok"; reasons says why.
func (s *Server) handleBoxSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if !s.requireScope(w, r, signalsReadScope) {
		return
	}
	account := strings.TrimSpace(principal.AccountScopeID)
	now := time.Now()
	var reasons []string
	status := BoxStatusOK
	raise := func(level, reason string) {
		reasons = append(reasons, reason)
		if level == BoxStatusDegraded || status == BoxStatusOK {
			status = level
		}
	}

	machine := map[string]any{"uptime_ms": now.Sub(s.startedAt).Milliseconds()}
	if cfg, err := s.loadStartupConfig(); err == nil && strings.TrimSpace(cfg.SwarmName) != "" {
		machine["name"] = strings.TrimSpace(cfg.SwarmName)
	}

	sb := sandbox.Default().Status()
	if !sb.Active {
		raise(BoxStatusDegraded, "agent sandbox is off: "+sb.Reason)
	}

	attention := map[string]any{"blocked_sessions": 0, "sessions": []boxBlockedSession{}}
	if s.perm != nil {
		summaries, err := s.perm.ListPendingSummaries(account, principal.UserID, 100_000)
		if err != nil {
			writeError(w, http.StatusInternalServerError, errors.New("read pending approvals: "+err.Error()))
			return
		}
		listed := make([]boxBlockedSession, 0, boxSummaryMaxBlocked)
		for _, summary := range summaries {
			if len(listed) < boxSummaryMaxBlocked {
				listed = append(listed, boxBlockedSession{SessionID: summary.SessionID, Pending: summary.PendingCount, OldestPendingAt: summary.OldestPendingAt})
			}
		}
		attention["blocked_sessions"] = len(summaries)
		attention["sessions"] = listed
		if len(summaries) > 0 {
			attention["oldest_pending_at"] = summaries[0].OldestPendingAt
			raise(BoxStatusAttention, "agents are waiting on a person")
		}
	}

	workers := map[string]any{"total": 0, "active_runs": 0, "today_runs": 0, "today_failed": 0, "failing": []boxWorker{}}
	if s.sessions != nil {
		list, err := s.sessions.ListWorkers(account, pebblestore.ListWorkersQuery{Limit: boxSummaryMaxWorkers})
		if err != nil {
			writeError(w, http.StatusInternalServerError, errors.New("read workers: "+err.Error()))
			return
		}
		day := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
		failing := make([]boxWorker, 0)
		active, runs, failed := 0, 0, 0
		for _, worker := range list.Workers {
			counts, err := s.sessions.SummarizeWorkerRuns(account, worker.ID, day, "UTC")
			if err != nil {
				writeError(w, http.StatusInternalServerError, errors.New("summarize worker runs: "+err.Error()))
				return
			}
			active += counts.ActiveRuns
			runs += counts.DailyRuns
			failed += counts.DailyFailed
			if counts.DailyFailed > 0 && len(failing) < boxSummaryMaxListed {
				failing = append(failing, boxWorker{WorkerID: worker.ID, Name: worker.Name, TodayRuns: counts.DailyRuns, TodayFailed: counts.DailyFailed, ActiveRuns: counts.ActiveRuns})
			}
		}
		workers = map[string]any{"total": len(list.Workers), "truncated": list.NextCursor != "", "active_runs": active, "today_runs": runs, "today_failed": failed, "failing": failing, "day": day.Format("2006-01-02")}
		if failed > 0 {
			raise(BoxStatusAttention, "workers failed today")
		}
	}

	usage := map[string]any{}
	if s.sessions != nil {
		limit, _, err := s.sessions.GetUsageLimit(account)
		if err != nil {
			writeError(w, http.StatusInternalServerError, errors.New("read usage limit: "+err.Error()))
			return
		}
		cost, tokens, err := s.sessions.GetTodayUsageTotal(account)
		if err != nil {
			writeError(w, http.StatusInternalServerError, errors.New("read today's usage: "+err.Error()))
			return
		}
		exceeded := limit.Enabled && limit.DailyCostLimitUSD > 0 && cost >= limit.DailyCostLimitUSD
		usage = map[string]any{"today_cost_usd": cost, "today_tokens": tokens, "daily_cost_limit_usd": limit.DailyCostLimitUSD, "limit_enabled": limit.Enabled, "limit_exceeded": exceeded}
		if exceeded {
			raise(BoxStatusDegraded, "daily spending limit reached")
		}
	}

	signalFeed := map[string]any{}
	if s.signals != nil {
		if page, err := s.signals.ListAfter(0, 1, pebblestore.SignalFilter{Account: account}); err == nil {
			signalFeed = map[string]any{"latest_seq": page.LatestSeq, "oldest_seq": page.OldestSeq}
		}
	}

	if reasons == nil {
		reasons = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"generated_at":    now.UnixMilli(),
		"status":          status,
		"reasons":         reasons,
		"machine":         machine,
		"sandbox":         map[string]any{"mode": string(sb.Mode), "active": sb.Active, "runtime": sb.Runtime, "reason": sb.Reason},
		"secrets_gateway": s.secretsGatewayEnabled,
		"permissions":     map[string]any{"bypass": s.permissionBypassForAccount(account), "bypass_blocked_reason": s.permissionBypassBlockedReason()},
		"attention":       attention,
		"workers":         workers,
		"usage":           usage,
		"signals":         signalFeed,
	})
}
