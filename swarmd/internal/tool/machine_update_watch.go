package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// manage_projects wait_machine watches one machine's update the way
// wait_environment watches a build: it records a durable watch on this
// conversation and returns at once. The conversation is woken (a note for a
// running turn, else a new turn) when
//   - the machine runs another commit (old -> new, with the update's reason);
//   - its update failed (failed or rolled back); or
//   - an update waits to be installed longer than stuck_minutes, or the
//     drain for it timed out, with why (runs blocking, drain timed out,
//     checks, install error).
//
// cancel_watch with the watch_id ends it. The watch is durable: when this
// machine itself updates, the daemon that starts on the new commit resumes
// the watch (ResumeMachineWatches) and its first read sees the new commit.

// FleetMachineWait is the machine update watch action.
const FleetMachineWait = "wait_machine"

// MachineWatchIDPrefix starts every machine watch id.
const MachineWatchIDPrefix = "machinewatch-"

// MachineWatchDefaultStuckMinutes is how long an update may wait to be
// installed before a watch wakes with stuck, when stuck_minutes is omitted.
const MachineWatchDefaultStuckMinutes = 30

// MachineWatchNextAction is the next step of a watch that returned at once.
const MachineWatchNextAction = "You will get a new turn when the machine runs a new commit, or its update fails or is stuck (with why); reply to the person now and end your turn; cancel_watch with watch_id stops it."

// Machine watch outcomes.
const (
	MachineWatchUpdated = "updated"
	MachineWatchFailed  = "failed"
	MachineWatchStuck   = "stuck"
)

// MachineUpdateReader reads a machine's update state: this machine (machine
// "") or a linked one, from the same machine summary machine_status shows.
// The API server provides it (ReadMachineUpdate).
type MachineUpdateReader interface {
	ReadMachineUpdate(ctx context.Context, p identity.Principal, machine string) (MachineUpdateView, error)
}

// MachineUpdateView is the part of a machine summary's machine object a
// watch reads: the running commit, the installer's update state and the
// update drain.
type MachineUpdateView struct {
	Commit string              `json:"commit"`
	Update *MachineUpdateState `json:"update,omitempty"`
	Drain  *MachineDrainState  `json:"drain,omitempty"`
}

// MachineUpdateState is machine.update: the installer's last word.
type MachineUpdateState struct {
	Auto      string `json:"auto,omitempty"`
	Channel   string `json:"channel,omitempty"`
	State     string `json:"state,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Running   string `json:"running,omitempty"`
	Candidate string `json:"candidate,omitempty"`
	At        int64  `json:"at,omitempty"`
}

// MachineDrainState is machine.drain: the update drain's progress.
type MachineDrainState struct {
	Draining     bool   `json:"draining"`
	TimedOut     bool   `json:"timed_out"`
	Ready        bool   `json:"ready"`
	Requested    bool   `json:"requested"`
	Force        bool   `json:"force"`
	Forced       bool   `json:"forced"`
	BlockingRuns int    `json:"blocking_runs"`
	SinceMs      int64  `json:"since_ms"`
	DeadlineMs   int64  `json:"deadline_ms"`
	EndReason    string `json:"end_reason,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// ParseMachineUpdateView reads a machine summary's machine object. Anything a
// linked machine says is untrusted: text is clipped.
func ParseMachineUpdateView(raw json.RawMessage) (MachineUpdateView, error) {
	var view MachineUpdateView
	if len(raw) == 0 || len(raw) > 256<<10 {
		return view, errors.New("the machine summary has no readable machine object")
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		return view, fmt.Errorf("the machine summary's machine object is unreadable: %w", err)
	}
	view.Commit = clipMachineText(view.Commit, 64)
	if u := view.Update; u != nil {
		u.State, u.Reason, u.Running, u.Candidate = clipMachineText(u.State, 32), clipMachineText(u.Reason, 300), clipMachineText(u.Running, 64), clipMachineText(u.Candidate, 64)
		u.Auto, u.Channel = clipMachineText(u.Auto, 8), clipMachineText(u.Channel, 100)
	}
	if d := view.Drain; d != nil {
		d.Reason, d.EndReason = clipMachineText(d.Reason, 400), clipMachineText(d.EndReason, 32)
	}
	return view, nil
}

func clipMachineText(v string, n int) string {
	v = strings.TrimSpace(v)
	if len(v) > n {
		v = v[:n]
	}
	return v
}

// running is the commit the machine runs: its daemon's own build, else what
// the installer says it runs.
func (v MachineUpdateView) running() string {
	if v.Commit != "" {
		return v.Commit
	}
	if v.Update != nil {
		return v.Update.Running
	}
	return ""
}

// sameCommit compares commits that may be full or short.
func sameCommit(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == b {
		return true
	}
	if len(a) < 7 || len(b) < 7 {
		return false
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

func shortCommit(c string) string {
	if len(c) > 9 {
		return c[:9]
	}
	return c
}

// MachineWatchEvent is what wakes a machine watch.
type MachineWatchEvent struct {
	Kind string `json:"kind"`
	// From is the commit the machine ran; To the one it runs now (updated)
	// or the candidate that is not installed (failed, stuck).
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	State string `json:"state,omitempty"`
	// Reason is the update's own reason (updated: "old -> new").
	Reason string `json:"reason,omitempty"`
	// Why says why an update failed or is stuck.
	Why          string `json:"why,omitempty"`
	WaitingSince int64  `json:"waiting_since,omitempty"`
}

// EvaluateMachineWatch decides whether a machine's state wakes its watch. It
// may update the watch's record (the running commit when it was unknown, the
// candidate it saw waiting and since when); changed says so, so the record is
// stored and a restart does not reset how long a candidate has waited.
func EvaluateMachineWatch(w *pebblestore.V3MachineWatchRecord, v MachineUpdateView, now time.Time) (*MachineWatchEvent, bool) {
	changed := false
	running := v.running()
	u := v.Update
	switch {
	case running == "":
	case w.Running == "":
		w.Running, changed = running, true
	case !sameCommit(running, w.Running):
		event := &MachineWatchEvent{Kind: MachineWatchUpdated, From: w.Running, To: running}
		if u != nil {
			event.State = u.State
			if u.State == "installed" || u.State == "current" {
				event.Reason = u.Reason
			}
		}
		if event.Reason == "" {
			event.Reason = shortCommit(w.Running) + " -> " + shortCommit(running)
		}
		return event, changed
	}
	// A drain seen still running is remembered: when it ends timed out the
	// watch wakes, whether it began before the watch or after.
	if d := v.Drain; d != nil && d.Draining && d.SinceMs != 0 && d.SinceMs != w.DrainSince {
		w.DrainSince, changed = d.SinceMs, true
	}
	pending := u != nil && u.Candidate != "" && !sameCommit(u.Candidate, running) && u.State != "current" && u.State != "installed"
	if !pending {
		if w.Candidate != "" || w.CandidateSince != 0 {
			w.Candidate, w.CandidateSince, changed = "", 0, true
		}
		return nil, changed
	}
	if (u.State == "failed" || u.State == "rolled_back") && u.At >= w.CreatedAt {
		return &MachineWatchEvent{Kind: MachineWatchFailed, From: running, To: u.Candidate, State: u.State, Why: "install error: " + u.Reason}, changed
	}
	if d := v.Drain; d != nil && d.EndReason == "expired" && (d.SinceMs >= w.CreatedAt || (w.DrainSince != 0 && d.SinceMs == w.DrainSince)) {
		why := "drain timed out"
		if d.Reason != "" {
			why += ": " + d.Reason
		}
		return &MachineWatchEvent{Kind: MachineWatchStuck, From: running, To: u.Candidate, State: u.State, Why: why, WaitingSince: w.CandidateSince}, changed
	}
	if w.CandidateSince == 0 || !sameCommit(w.Candidate, u.Candidate) {
		w.Candidate, w.CandidateSince = u.Candidate, now.UnixMilli()
		return nil, true
	}
	stuckAfter := w.StuckAfterMinutes
	if stuckAfter <= 0 {
		stuckAfter = MachineWatchDefaultStuckMinutes
	}
	if now.Sub(time.UnixMilli(w.CandidateSince)) < time.Duration(stuckAfter)*time.Minute {
		return nil, changed
	}
	return &MachineWatchEvent{Kind: MachineWatchStuck, From: running, To: u.Candidate, State: u.State, Why: machineStuckWhy(u, v.Drain), WaitingSince: w.CandidateSince}, changed
}

// machineStuckWhy says why a waiting update is not installed.
func machineStuckWhy(u *MachineUpdateState, d *MachineDrainState) string {
	if d != nil && d.Draining {
		why := fmt.Sprintf("draining for the update: %d agent run(s) still running", d.BlockingRuns)
		if d.Reason != "" {
			why = "draining for the update: " + d.Reason
		}
		return why
	}
	label := map[string]string{
		"waiting":        "runs blocking or waiting",
		"waiting_review": "waiting for its checks",
		"blocked":        "blocked by a failed check",
		"building":       "still building",
		"draining":       "draining",
	}[u.State]
	if label == "" {
		label = u.State
	}
	if u.Reason == "" {
		return label
	}
	return label + ": " + u.Reason
}

// machineWatchStore is the durable state of machine watches (the session
// store).
type machineWatchStore interface {
	PutV3MachineWatch(pebblestore.V3MachineWatchRecord) error
	GetV3MachineWatch(sessionID, id string) (pebblestore.V3MachineWatchRecord, bool, error)
	DeleteV3MachineWatch(sessionID, id string) error
	ListV3MachineWatches(sessionID string) ([]pebblestore.V3MachineWatchRecord, error)
}

// machineWatches are the machine watches followed in this process.
type machineWatches struct {
	mu        sync.Mutex
	following map[string]*environmentFollow
	// interval is how often a follower reads the machine (default
	// machineWatchInterval); SetMachineWatchInterval shortens it.
	interval time.Duration
	// delivered, when set by a test, is told each delivered watch.
	delivered func(id string)
}

const (
	// machineWatchInterval: a follower reads the machine's summary this
	// often. The installer writes its state at most every few minutes.
	machineWatchInterval = time.Minute
	// machineWatchUnreachableGrace: a machine that does not answer for this
	// long (an update restarts it for a minute or two) ends the watch.
	machineWatchUnreachableGrace = 30 * time.Minute
)

// SetMachineWatchInterval sets how often machine watch followers read their
// machine (a test shortens it; zero restores the default).
func (r *Runtime) SetMachineWatchInterval(d time.Duration) {
	if r == nil {
		return
	}
	r.machineWatches.mu.Lock()
	defer r.machineWatches.mu.Unlock()
	r.machineWatches.interval = d
}

func (r *Runtime) machineWatchInterval() time.Duration {
	r.machineWatches.mu.Lock()
	defer r.machineWatches.mu.Unlock()
	if r.machineWatches.interval > 0 {
		return r.machineWatches.interval
	}
	return machineWatchInterval
}

func machineWatchID(sessionID, machine string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{sessionID, machine}, "\x00")))
	return MachineWatchIDPrefix + hex.EncodeToString(sum[:12])
}

func (r *Runtime) machineWatchStore() (machineWatchStore, error) {
	if r == nil {
		return nil, errors.New("machine watches are not configured")
	}
	store, ok := r.backgroundWork.(machineWatchStore)
	if !ok {
		return nil, errors.New("machine watches are not configured: no durable store")
	}
	return store, nil
}

func (r *Runtime) machineUpdateReader() (MachineUpdateReader, error) {
	reader, ok := r.getProjectTaskLifecycleService().(MachineUpdateReader)
	if !ok {
		return nil, errors.New("machine update service unavailable")
	}
	return reader, nil
}

// executeMachineWatch is wait_machine: the machine's update is watched from
// its state now.
func (r *Runtime) executeMachineWatch(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	if scope.ControlCaller {
		return "", errors.New("wait_machine wakes a conversation, and a Swarm Control caller has none: manage_fleet status (machine.update and machine.drain) reads a machine's update")
	}
	if r == nil || r.sessions == nil || !scope.Principal.Valid() || scope.Principal.Type != identity.PrincipalTypeUser {
		return "", errors.New("wait_machine requires an authenticated session")
	}
	if _, err := r.homeConversation(scope, "machine watch session ownership mismatch", "machine updates are watched by the Orchestrator, not by tasks"); err != nil {
		return "", err
	}
	run, ok := VideoRunContextFromContext(ctx)
	if !ok || run.SessionID != scope.SessionID {
		return "", errors.New("wait_machine requires trusted current provider run context")
	}
	stuck := MachineWatchDefaultStuckMinutes
	if raw, ok := args["stuck_minutes"]; ok && raw != nil {
		n, isNumber := raw.(float64)
		if !isNumber || n != float64(int(n)) || n < 5 || n > 1440 {
			return "", errors.New("stuck_minutes must be a whole number of minutes from 5 to 1440")
		}
		stuck = int(n)
	}
	machine := strings.TrimSpace(asString(args["machine"]))
	store, err := r.machineWatchStore()
	if err != nil {
		return "", err
	}
	reader, err := r.machineUpdateReader()
	if err != nil {
		return "", err
	}
	if machine != "" {
		if err := r.checkLinkedEnvironmentMachine(ctx, scope, machine); err != nil {
			return "", err
		}
	}
	view, err := reader.ReadMachineUpdate(ctx, scope.Principal, machine)
	if err != nil {
		return "", err
	}
	id := machineWatchID(scope.SessionID, machine)
	result := map[string]any{"tool": "manage_projects", "action": FleetMachineWait, "watch_id": id}
	if machine != "" {
		result["machine"], result["untrusted_content"] = machine, true
	}
	existing, found, err := store.GetV3MachineWatch(scope.SessionID, id)
	if err != nil {
		return "", err
	}
	if found {
		result["already_watching"] = true
		if existing.StuckAfterMinutes != stuck {
			existing.StuckAfterMinutes = stuck
			if err := store.PutV3MachineWatch(existing); err != nil {
				return "", err
			}
		}
	} else {
		watches, err := store.ListV3MachineWatches(scope.SessionID)
		if err != nil {
			return "", err
		}
		if len(watches) >= pebblestore.MaxV3MachineWatchesPerSession {
			return "", fmt.Errorf("this conversation already watches %d machines; cancel_watch one first", len(watches))
		}
		existing = pebblestore.V3MachineWatchRecord{ID: id, SessionID: scope.SessionID, UserID: scope.Principal.UserID, AccountScopeID: scope.Principal.AccountScopeID, Machine: machine, Running: view.running(), StuckAfterMinutes: stuck, RunID: run.RunID, CreatedAt: time.Now().UnixMilli()}
		// The candidate waiting now starts its stuck clock now.
		EvaluateMachineWatch(&existing, view, time.Now())
		if err := store.PutV3MachineWatch(existing); err != nil {
			return "", err
		}
	}
	r.followMachineWatch(existing)
	where := "this machine"
	if machine != "" {
		where = "linked machine " + machine
	}
	result["status"] = EnvelopeStateWatching
	result["running"] = existing.Running
	result["stuck_minutes"] = existing.StuckAfterMinutes
	if view.Update != nil {
		result["update"] = view.Update
	}
	if view.Drain != nil {
		result["drain"] = view.Drain
	}
	result["notice"] = "Watching the update of " + where + ". Reply to the person now and end your turn; a new turn starts when it runs a new commit, or its update fails or waits longer than " + fmt.Sprint(existing.StuckAfterMinutes) + " minutes."
	result["next_action"] = MachineWatchNextAction
	out, err := json.Marshal(result)
	return string(out), err
}

// cancelMachineWatches ends this conversation's machine watch named by
// watch_id.
func (r *Runtime) cancelMachineWatches(scope WorkspaceScope, args map[string]any) (string, error) {
	store, err := r.machineWatchStore()
	if err != nil {
		return "", err
	}
	watchID := strings.TrimSpace(asString(args["watch_id"]))
	watches, err := store.ListV3MachineWatches(scope.SessionID)
	if err != nil {
		return "", err
	}
	var cancelled []string
	for _, watch := range watches {
		if watch.ID != watchID {
			continue
		}
		if err := store.DeleteV3MachineWatch(watch.SessionID, watch.ID); err != nil {
			return "", err
		}
		r.stopFollowingMachineWatch(watch.ID)
		cancelled = append(cancelled, watch.ID)
	}
	if len(cancelled) == 0 {
		return "", errors.New("no machine watch on this conversation matches; nothing was cancelled")
	}
	out, err := json.Marshal(map[string]any{"tool": "manage_projects", "action": "cancel_watch", "status": EnvelopeStateCancelled, "watch_ids": cancelled})
	return string(out), err
}

// followMachineWatch follows a recorded watch in the background until it
// wakes its conversation or is cancelled. A watch already followed here is
// not followed twice.
func (r *Runtime) followMachineWatch(watch pebblestore.V3MachineWatchRecord) {
	r.machineWatches.mu.Lock()
	if r.machineWatches.following == nil {
		r.machineWatches.following = map[string]*environmentFollow{}
	}
	if _, ok := r.machineWatches.following[watch.ID]; ok {
		r.machineWatches.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	follow := &environmentFollow{cancel: cancel}
	r.machineWatches.following[watch.ID] = follow
	r.machineWatches.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			r.machineWatches.mu.Lock()
			if r.machineWatches.following[watch.ID] == follow {
				delete(r.machineWatches.following, watch.ID)
			}
			r.machineWatches.mu.Unlock()
		}()
		r.runMachineWatch(ctx, watch)
	}()
}

func (r *Runtime) stopFollowingMachineWatch(id string) {
	r.machineWatches.mu.Lock()
	defer r.machineWatches.mu.Unlock()
	if follow, ok := r.machineWatches.following[id]; ok {
		follow.cancel()
		delete(r.machineWatches.following, id)
	}
}

func (r *Runtime) stopMachineWatches() {
	r.machineWatches.mu.Lock()
	defer r.machineWatches.mu.Unlock()
	for id, follow := range r.machineWatches.following {
		follow.cancel()
		delete(r.machineWatches.following, id)
	}
}

// runMachineWatch reads the machine now and then every interval until an
// outcome wakes the conversation. A machine that does not answer (it may be
// restarting into its update) is read again; after
// machineWatchUnreachableGrace without an answer the watch ends with why.
func (r *Runtime) runMachineWatch(ctx context.Context, watch pebblestore.V3MachineWatchRecord) {
	reader, err := r.machineUpdateReader()
	if err != nil {
		log.Printf("swarmd machine watch %s: %v", watch.ID, err)
		return
	}
	store, err := r.machineWatchStore()
	if err != nil {
		log.Printf("swarmd machine watch %s: %v", watch.ID, err)
		return
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: watch.UserID, AccountScopeID: watch.AccountScopeID}
	var unreachableSince time.Time
	for {
		view, readErr := reader.ReadMachineUpdate(ctx, principal, watch.Machine)
		if ctx.Err() != nil {
			return // cancelled, or the daemon is stopping: the record stays for a restart
		}
		current, found, err := store.GetV3MachineWatch(watch.SessionID, watch.ID)
		if err != nil || !found || current.CreatedAt != watch.CreatedAt {
			return // cancelled (or replaced)
		}
		watch = current
		var event *MachineWatchEvent
		if readErr != nil {
			if unreachableSince.IsZero() {
				unreachableSince = time.Now()
			}
			if time.Since(unreachableSince) >= machineWatchUnreachableGrace {
				r.deliverMachineWatch(principal, store, watch, nil, readErr)
				return
			}
		} else {
			unreachableSince = time.Time{}
			var changed bool
			event, changed = EvaluateMachineWatch(&watch, view, time.Now())
			if event != nil {
				r.deliverMachineWatch(principal, store, watch, event, nil)
				return
			}
			if changed {
				if err := store.PutV3MachineWatch(watch); err != nil {
					log.Printf("swarmd machine watch %s record: %v", watch.ID, err)
				}
			}
		}
		if err := sleepMachineWatch(ctx, r.machineWatchInterval()); err != nil {
			return
		}
	}
}

func sleepMachineWatch(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// deliverMachineWatch wakes the conversation with the outcome (or why the
// watch ended without one) and ends the watch.
func (r *Runtime) deliverMachineWatch(principal identity.Principal, store machineWatchStore, watch pebblestore.V3MachineWatchRecord, event *MachineWatchEvent, readErr error) {
	content := machineWatchMessage(watch, event, readErr)
	metadata := map[string]any{"machine_watch_id": watch.ID, "untrusted_content": true}
	kind := "unreachable"
	if event != nil {
		kind = event.Kind
		metadata["machine_update"] = event
	}
	metadata["machine_watch_outcome"] = kind
	if watch.Machine != "" {
		metadata["machine"] = watch.Machine
	}
	to := ""
	if event != nil {
		to = event.To
	}
	wakeID := fmt.Sprintf("%s:%d:%s:%s", watch.ID, watch.CreatedAt, kind, to)
	_, err := r.deliverSessionWake(principal, watch.SessionID, "machine-watch:"+wakeID, &pebblestore.MessageSnapshot{Role: "user", Content: content, Metadata: metadata}, &pebblestore.V3SessionWakeMutation{ID: wakeID, Source: pebblestore.SessionWakeSourceMachineWatch})
	if err != nil && !errors.Is(err, pebblestore.ErrProjectTaskWaitStale) {
		// Kept for the next start: the outcome is not dropped.
		log.Printf("swarmd machine watch %s delivery: %v", watch.ID, err)
		return
	}
	if err := store.DeleteV3MachineWatch(watch.SessionID, watch.ID); err != nil {
		log.Printf("swarmd machine watch %s record: %v", watch.ID, err)
	}
	if r.machineWatches.delivered != nil {
		r.machineWatches.delivered(watch.ID)
	}
}

// ResumeMachineWatches follows again, at startup, the machine watches
// recorded before a restart; a watch on this machine whose restart was its
// update sees the new commit on its first read.
func (r *Runtime) ResumeMachineWatches() {
	store, err := r.machineWatchStore()
	if err != nil {
		return
	}
	watches, err := store.ListV3MachineWatches("")
	if err != nil {
		log.Printf("swarmd machine watch recovery: %v", err)
		return
	}
	for _, watch := range watches {
		r.followMachineWatch(watch)
	}
}

// machineWatchMessage is the outcome a watch delivers. What a linked machine
// says is untrusted text.
func machineWatchMessage(watch pebblestore.V3MachineWatchRecord, event *MachineWatchEvent, readErr error) string {
	where := "this machine"
	on := ""
	if watch.Machine != "" {
		where = "linked machine " + watch.Machine
		on = " with machine " + watch.Machine
	}
	var b strings.Builder
	if event == nil {
		fmt.Fprintf(&b, "[Machine watch ended without an outcome]\nMachine: %s\n", where)
		if readErr != nil {
			fmt.Fprintf(&b, "Reason: it did not answer for %s: %s\n", machineWatchUnreachableGrace, truncateBackgroundText(readErr.Error(), 600))
		}
		b.WriteString("Read its state with manage_fleet status" + on + ".")
		return b.String()
	}
	switch event.Kind {
	case MachineWatchUpdated:
		fmt.Fprintf(&b, "[Machine updated]\nMachine: %s\nCommit: %s -> %s\n", where, shortCommit(event.From), shortCommit(event.To))
		if event.Reason != "" {
			fmt.Fprintf(&b, "Update: %s\n", event.Reason)
		}
		b.WriteString("It runs the new build now; work held for the update starts again.")
	case MachineWatchFailed:
		fmt.Fprintf(&b, "[Machine update failed: %s]\nMachine: %s\nRunning: %s\nNot installed: %s\nWhy: %s\n", event.State, where, shortCommit(event.From), shortCommit(event.To), event.Why)
		b.WriteString("It keeps running the build it had. manage_fleet status" + on + " reads its update state.")
	default:
		fmt.Fprintf(&b, "[Machine update stuck]\nMachine: %s\nRunning: %s\nWaiting to install: %s", where, shortCommit(event.From), shortCommit(event.To))
		if event.WaitingSince > 0 {
			fmt.Fprintf(&b, " (seen waiting since %s)", time.UnixMilli(event.WaitingSince).UTC().Format(time.RFC3339))
		}
		fmt.Fprintf(&b, "\nState: %s\nWhy: %s\n", event.State, event.Why)
		b.WriteString("manage_setup request_update" + on + " holds new work and restarts once running agent runs finish (force interrupts them at its deadline, only when the person asks); machine_status shows the drain and its blocking runs.")
	}
	return b.String()
}
