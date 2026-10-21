package api

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/buildinfo"
	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	"swarm/packages/swarmd/internal/sandbox"
	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Box summary bounds: the summary is one cheap call a monitor makes per
// machine, so it lists at most this many blocked sessions and workers.
const (
	boxSummaryMaxBlocked   = 20
	boxSummaryMaxWorkers   = 200
	boxSummaryMaxListed    = 20
	boxSummaryMaxSchedules = 100
)

// Box status values, from best to worst.
const (
	BoxStatusOK        = "ok"
	BoxStatusAttention = "attention" // something waits on a person or failed today
	BoxStatusDegraded  = "degraded"  // a wanted sandbox could not start, or spending is over the limit
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
// holds ids, names and counts only. Status is "degraded" when a sandbox
// that should be on could not start, or spending is over the limit, "attention" when something waits
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

	machine := map[string]any{"uptime_ms": now.Sub(s.startedAt).Milliseconds(), "version": strings.TrimSpace(buildinfo.Version), "commit": strings.TrimSpace(buildinfo.Commit)}
	if cfg, err := s.loadStartupConfig(); err == nil && strings.TrimSpace(cfg.SwarmName) != "" {
		machine["name"] = strings.TrimSpace(cfg.SwarmName)
	}
	if update := boxUpdateState(); update != nil {
		machine["update"] = update
		if update.State == "rolled_back" {
			raise(BoxStatusAttention, "an update was rolled back: "+update.Reason)
		}
	}
	if manager := s.ExecutionCapacity(); manager != nil {
		drain := manager.DrainStatus()
		// Machine summaries expose no cross-account run identifiers.
		machine["drain"] = map[string]any{"draining": drain.Draining, "since": drain.Since, "deadline": drain.Deadline, "timed_out": drain.TimedOut, "blocking_runs": len(drain.Blockers)}
		if drain.Draining {
			raise(BoxStatusAttention, "draining for update")
		}
	}
	busy, err := s.boxBusy()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	sb := sandbox.Default().Status()
	if !sb.Active {
		if sb.Mode == sandbox.ModeRequired {
			raise(BoxStatusDegraded, "agent execution blocked: required sandbox unavailable: "+sb.Reason)
		} else if sb.Reason != "sandbox mode is off" {
			// Off by the owner's setting is the headless default, not a fault;
			// a sandbox that was wanted but could not start is.
			raise(BoxStatusDegraded, "agent sandbox is off: "+sb.Reason)
		}
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
	var automations map[string]any
	var runs pebblestore.WorkerRunCounts
	if s.sessions != nil {
		window, err := boxSummaryWindow(r, now)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		all, err := s.allWorkers(account)
		if err != nil {
			writeError(w, http.StatusInternalServerError, errors.New("read workers: "+err.Error()))
			return
		}
		runs, err = s.sessions.CountWorkerRuns(account, window.since, window.until)
		if err != nil {
			writeError(w, http.StatusInternalServerError, errors.New("count worker runs: "+err.Error()))
			return
		}
		names := map[string]string{}
		byState := map[string]int{}
		for _, worker := range all {
			names[worker.ID] = worker.Name
			byState[string(worker.LifecycleState)]++
		}
		automations, err = boxAutomations(all, now)
		if err != nil {
			writeError(w, http.StatusInternalServerError, errors.New("read schedules: "+err.Error()))
			return
		}
		failing := make([]boxWorker, 0)
		for id, tally := range runs.ByWorker {
			if tally.Failed > 0 {
				failing = append(failing, boxWorker{WorkerID: id, Name: names[id], TodayRuns: tally.Runs, TodayFailed: tally.Failed, ActiveRuns: tally.Running})
			}
		}
		sort.Slice(failing, func(i, j int) bool {
			return failing[i].TodayFailed > failing[j].TodayFailed || (failing[i].TodayFailed == failing[j].TodayFailed && failing[i].WorkerID < failing[j].WorkerID)
		})
		if len(failing) > boxSummaryMaxListed {
			failing = failing[:boxSummaryMaxListed]
		}
		for i := range runs.Failed {
			runs.Failed[i].WorkerName = names[runs.Failed[i].WorkerID]
		}
		for i := range runs.RunningRuns {
			runs.RunningRuns[i].WorkerName = names[runs.RunningRuns[i].WorkerID]
		}
		workers = map[string]any{"total": len(all), "by_state": byState, "truncated": false, "active_runs": runs.Running, "today_runs": runs.Total, "today_failed": runs.ByStatus["failed"], "failing": failing, "day": window.day, "timezone": window.zone}
		if runs.ByStatus["failed"] > 0 {
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

	keys := s.boxKeys(account, now, raise)

	exportState, hasOutbox := s.outboxState()
	if hasOutbox && exportState.Failing {
		raise(BoxStatusAttention, "the outbox cannot export: "+exportState.LastError)
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
		"busy":            busy,
		"sandbox":         map[string]any{"mode": string(sb.Mode), "active": sb.Active, "runtime": sb.Runtime, "reason": sb.Reason},
		"secrets_gateway": s.secretsGatewayEnabled,
		"permissions":     map[string]any{"bypass": s.permissionBypassForAccount(account), "bypass_blocked_reason": s.permissionBypassBlockedReason()},
		"attention":       attention,
		"workers":         workers,
		"runs":            boxRuns(runs, s.sessions != nil),
		"automations":     automations,
		"workspaces":      s.boxWorkspaces(principal),
		"usage":           usage,
		"keys":            keys,
		"signals":         signalFeed,
		"outbox":          exportState,
	})
}

// boxWindow is the run-count window: today in a timezone (default UTC), or
// explicit since/until milliseconds.
type boxWindow struct {
	since, until int64
	day, zone    string
}

// boxSummaryWindow reads ?timezone=Area/City (today there) or ?since=&until=
// (milliseconds, until exclusive; 0 or absent is open).
func boxSummaryWindow(r *http.Request, now time.Time) (boxWindow, error) {
	q := r.URL.Query()
	if raw := strings.TrimSpace(q.Get("since")); raw != "" {
		since, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || since < 0 {
			return boxWindow{}, errors.New("since must be milliseconds since the epoch")
		}
		until := int64(0)
		if raw := strings.TrimSpace(q.Get("until")); raw != "" {
			if until, err = strconv.ParseInt(raw, 10, 64); err != nil || (until != 0 && until <= since) {
				return boxWindow{}, errors.New("until must be milliseconds after since")
			}
		}
		return boxWindow{since: since, until: until}, nil
	}
	zone := strings.TrimSpace(q.Get("timezone"))
	if zone == "" {
		zone = "UTC"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return boxWindow{}, errors.New("timezone must be an IANA name such as UTC or Europe/Helsinki")
	}
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return boxWindow{since: start.UnixMilli(), until: start.AddDate(0, 0, 1).UnixMilli(), day: start.Format("2006-01-02"), zone: zone}, nil
}

// allWorkers lists every worker that is not deleted, all pages.
func (s *Server) allWorkers(account string) ([]pebblestore.WorkerRecord, error) {
	all := []pebblestore.WorkerRecord{}
	after := ""
	for {
		page, err := s.sessions.ListWorkers(account, pebblestore.ListWorkersQuery{Limit: 100, After: after})
		if err != nil {
			return nil, err
		}
		all = append(all, page.Workers...)
		if page.NextCursor == "" || page.NextCursor == after {
			return all, nil
		}
		after = page.NextCursor
	}
}

type boxSchedule struct {
	WorkerID     string `json:"worker_id"`
	WorkerName   string `json:"worker_name"`
	AutomationID string `json:"automation_id"`
	Name         string `json:"name"`
	Mode         string `json:"mode"`
	Cron         string `json:"cron,omitempty"`
	Interval     int64  `json:"interval_seconds,omitempty"`
	Timezone     string `json:"timezone,omitempty"`
	Enabled      bool   `json:"enabled"`
	Active       bool   `json:"active"`
	NextRunAt    int64  `json:"next_run_at,omitempty"`
}

// boxAutomations counts the schedules (cron and interval automations) of the
// workers: all of them, the enabled ones, and the active ones (enabled on an
// active worker: they will run), with the next run of each active one.
func boxAutomations(all []pebblestore.WorkerRecord, now time.Time) (map[string]any, error) {
	schedules := []boxSchedule{}
	enabled, active, triggers := 0, 0, 0
	next := int64(0)
	for _, worker := range all {
		for _, a := range worker.Automations {
			if a.ActivationMode == "external_trigger" {
				triggers++
				continue
			}
			if a.Schedule == nil || (a.ActivationMode != "interval" && a.ActivationMode != "cron") {
				continue
			}
			item := boxSchedule{WorkerID: worker.ID, WorkerName: worker.Name, AutomationID: a.ID, Name: a.Name, Mode: a.ActivationMode, Cron: a.Schedule.Cron, Interval: a.Schedule.IntervalSeconds, Timezone: a.Schedule.Timezone, Enabled: a.Enabled}
			if a.Enabled {
				enabled++
			}
			if a.Enabled && worker.LifecycleState == pebblestore.WorkerLifecycleStateActive {
				one := worker
				one.Automations = []pebblestore.WorkerAutomationDefinition{a}
				at, err := runruntime.NextWorkerScheduledAt(one, now)
				if err != nil {
					return nil, err
				}
				item.Active, item.NextRunAt = true, at
				active++
				if at > 0 && (next == 0 || at < next) {
					next = at
				}
			}
			schedules = append(schedules, item)
		}
	}
	sort.Slice(schedules, func(i, j int) bool {
		if schedules[i].Active != schedules[j].Active {
			return schedules[i].Active
		}
		return schedules[i].NextRunAt < schedules[j].NextRunAt || (schedules[i].NextRunAt == schedules[j].NextRunAt && schedules[i].AutomationID < schedules[j].AutomationID)
	})
	listed := schedules
	if len(listed) > boxSummaryMaxSchedules {
		listed = listed[:boxSummaryMaxSchedules]
	}
	return map[string]any{"schedules": len(schedules), "enabled": enabled, "active": active, "triggers": triggers, "next_run_at": next, "list": listed}, nil
}

// boxRuns is the run-count block: exact counts for the window, the failed runs
// (newest first) and everything running now.
func boxRuns(runs pebblestore.WorkerRunCounts, available bool) map[string]any {
	if !available {
		return map[string]any{}
	}
	return map[string]any{
		"since": runs.SinceMs, "until": runs.UntilMs, "total": runs.Total,
		"succeeded": runs.ByStatus["succeeded"], "failed": runs.ByStatus["failed"], "cancelled": runs.ByStatus["cancelled"],
		"unfinished": runs.ByStatus["admitted"] + runs.ByStatus["running"],
		"by_status":  runs.ByStatus, "by_source": runs.BySource,
		"failed_runs": runs.Failed, "running_now": runs.Running, "running_runs": runs.RunningRuns,
	}
}

// boxWorkspaces counts the folders registered on this machine.
func (s *Server) boxWorkspaces(principal identity.Principal) map[string]any {
	if s.workspace == nil {
		return map[string]any{}
	}
	entries, err := s.workspace.ListKnownForPrincipal(principal, 100000)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	names := []string{}
	for _, entry := range entries {
		if len(names) < boxSummaryMaxListed {
			names = append(names, firstNonEmpty(entry.WorkspaceName, entry.Path))
		}
	}
	return map[string]any{"total": len(entries), "names": names}
}

// boxKeys is the keys part of the summary: active keys by kind, keys that
// stop working on their own within a week, and keys used from a new place in
// the last day, which also raise attention.
func (s *Server) boxKeys(account string, now time.Time, raise func(level, reason string)) map[string]any {
	keys := map[string]any{"active": map[string]int{}, "stopping_soon": []map[string]any{}, "new_places": []map[string]any{}}
	if s.security == nil {
		return keys
	}
	records, err := s.security.ListScopedTokens(account)
	if err != nil {
		return keys
	}
	nowMs := now.UnixMilli()
	active := map[string]int{}
	stopping := []map[string]any{}
	newPlaces := []map[string]any{}
	for _, record := range records {
		item := newScopedTokenListItem(record, nowMs)
		if item.KeyKind == security.KeyKindInternal || item.State != "active" {
			continue
		}
		active[item.KeyKind]++
		if item.LapsesAt > 0 && item.LapsesAt-nowMs <= security.KeyExpiringNotice.Milliseconds() {
			stopping = append(stopping, map[string]any{"id": record.ID, "name": record.Name, "kind": item.KeyKind, "stops_working_at": item.LapsesAt, "because": item.LapseReason})
		}
		if record.NewSourceAt > 0 && nowMs-record.NewSourceAt <= (24*time.Hour).Milliseconds() {
			newPlaces = append(newPlaces, map[string]any{"id": record.ID, "name": record.Name, "kind": item.KeyKind, "from": record.LastUsedFrom, "at": record.NewSourceAt})
		}
	}
	if len(stopping) > 0 {
		raise(BoxStatusAttention, fmt.Sprintf("%d key(s) stop working within 7 days (swarm_list_keys)", len(stopping)))
	}
	if len(newPlaces) > 0 {
		raise(BoxStatusAttention, fmt.Sprintf("%d key(s) used from a new place today; revoke any the person does not recognize (swarm_list_keys)", len(newPlaces)))
	}
	keys["active"], keys["stopping_soon"], keys["new_places"] = active, stopping, newPlaces
	return keys
}
