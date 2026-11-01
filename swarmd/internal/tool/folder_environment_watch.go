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

// manage_projects wait_environment watches one folder environment build the
// way wait_tasks watches tasks: it records a durable watch on this
// conversation and returns at once; when the build ends (ready or failed)
// the outcome reaches the conversation through the session wake (a note for
// a running turn, else a new turn) with its status, duration, image and the
// last lines of its log. cancel_watch with workspace_path ends it. Watches
// survive a restart (ResumeEnvironmentWatches); a conversation archived or
// deleted drops them.

// FolderEnvironmentWait is the environment watch action.
const FolderEnvironmentWait = "wait_environment"

// FolderEnvironmentWatcher follows a folder environment build until it ends
// and returns get_environment's answer with the log. The API server
// provides it (AwaitFolderEnvironmentBuild), here or on a linked machine.
type FolderEnvironmentWatcher interface {
	AwaitFolderEnvironmentBuild(ctx context.Context, p identity.Principal, machine, path string) (json.RawMessage, error)
}

// environmentWatchStore is the durable state of environment watches (the
// session store).
type environmentWatchStore interface {
	PutV3EnvironmentWatch(pebblestore.V3EnvironmentWatchRecord) error
	GetV3EnvironmentWatch(sessionID, id string) (pebblestore.V3EnvironmentWatchRecord, bool, error)
	DeleteV3EnvironmentWatch(sessionID, id string) error
	ListV3EnvironmentWatches(sessionID string) ([]pebblestore.V3EnvironmentWatchRecord, error)
}

// EnvironmentWatchLogLines is how many of the build log's last lines the
// outcome carries.
const EnvironmentWatchLogLines = 40

// EnvironmentWatchNextAction is the next step of a watch that returned at
// once.
const EnvironmentWatchNextAction = "You will get a new turn when the build ends (ready or failed) with its outcome and log tail; reply to the person now and end your turn; cancel_watch with workspace_path stops it."

// environmentWatches are the watches followed in this process.
type environmentWatches struct {
	mu        sync.Mutex
	following map[string]*environmentFollow
	// delivered, when set by a test, is told each watch whose outcome was
	// delivered (or dropped with its conversation).
	delivered func(id string)
}

// environmentFollow is one follower of a watch; a follower removes only its
// own entry, never a newer one registered after a cancel.
type environmentFollow struct{ cancel context.CancelFunc }

func environmentWatchID(sessionID, machine, path string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{sessionID, machine, path}, "\x00")))
	return "envwatch-" + hex.EncodeToString(sum[:12])
}

func (r *Runtime) environmentWatchStore() (environmentWatchStore, error) {
	if r == nil {
		return nil, errors.New("environment watches are not configured")
	}
	store, ok := r.backgroundWork.(environmentWatchStore)
	if !ok {
		return nil, errors.New("environment watches are not configured: no durable store")
	}
	return store, nil
}

func (r *Runtime) environmentWatcher() (FolderEnvironmentWatcher, error) {
	watcher, ok := r.getProjectTaskLifecycleService().(FolderEnvironmentWatcher)
	if !ok {
		return nil, errors.New("folder environment service unavailable")
	}
	return watcher, nil
}

// executeEnvironmentWatch is wait_environment: the folder's build is
// watched, or, when no build runs, its last outcome is answered at once.
func (r *Runtime) executeEnvironmentWatch(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	if scope.ControlCaller {
		return "", errors.New("wait_environment wakes a conversation, and a Swarm Control caller has none: use get_environment with wait_seconds (at most 110) to wait for the build")
	}
	if r == nil || r.sessions == nil || !scope.Principal.Valid() || scope.Principal.Type != identity.PrincipalTypeUser {
		return "", errors.New("wait_environment requires an authenticated session")
	}
	if _, err := r.homeConversation(scope, "folder environment session ownership mismatch", "folder environments are managed by the Orchestrator, not by tasks; agents in a folder use lease_environment"); err != nil {
		return "", err
	}
	run, ok := VideoRunContextFromContext(ctx)
	if !ok || run.SessionID != scope.SessionID {
		return "", errors.New("wait_environment requires trusted current provider run context")
	}
	path := strings.TrimSpace(asString(args["workspace_path"]))
	if path == "" {
		return "", errors.New("wait_environment needs workspace_path: the absolute path of the folder (list_environments shows them)")
	}
	machine := strings.TrimSpace(asString(args["machine"]))
	service, ok := r.getProjectTaskLifecycleService().(FolderEnvironments)
	if !ok {
		return "", errors.New("folder environment service unavailable")
	}
	store, err := r.environmentWatchStore()
	if err != nil {
		return "", err
	}
	if _, err := r.environmentWatcher(); err != nil {
		return "", err
	}
	if machine != "" {
		if err := r.checkLinkedEnvironmentMachine(ctx, scope, machine); err != nil {
			return "", err
		}
	}
	// The folder's state now: a build that already ended is answered here.
	raw, err := service.ManageFolderEnvironment(ctx, scope.Principal, machine, FolderEnvironmentGet, map[string]any{"workspace_path": path, "include_log": true})
	if err != nil {
		return "", err
	}
	outcome := environmentOutcomeFrom(raw)
	result := map[string]any{"tool": "manage_projects", "action": FolderEnvironmentWait, "workspace_path": path}
	if machine != "" {
		result["machine"], result["untrusted_content"] = machine, true
	}
	if !outcome.Defined {
		return "", fmt.Errorf("%s has no environment (it runs on Swarm's default); there is no build to watch", path)
	}
	if outcome.Status != pebblestore.SandboxEnvironmentBuilding {
		result["status"] = EnvelopeStateOK
		result["build_status"] = outcome.Status
		result["watching"] = false
		result["outcome"] = outcome.view(EnvironmentWatchLogLines)
		result["next_action"] = "No build is running: this is the last build's outcome. update_environment or rebuild_environment starts one."
		out, err := json.Marshal(result)
		return string(out), err
	}
	id := environmentWatchID(scope.SessionID, machine, path)
	existing, found, err := store.GetV3EnvironmentWatch(scope.SessionID, id)
	if err != nil {
		return "", err
	}
	if found {
		result["already_watching"] = true
	} else {
		watches, err := store.ListV3EnvironmentWatches(scope.SessionID)
		if err != nil {
			return "", err
		}
		if len(watches) >= pebblestore.MaxV3EnvironmentWatchesPerSession {
			return "", fmt.Errorf("this conversation already watches %d environment builds; cancel_watch one first", len(watches))
		}
		existing = pebblestore.V3EnvironmentWatchRecord{ID: id, SessionID: scope.SessionID, UserID: scope.Principal.UserID, AccountScopeID: scope.Principal.AccountScopeID, Machine: machine, WorkspacePath: path, Hash: outcome.Hash, RunID: run.RunID, CreatedAt: time.Now().UnixMilli()}
		if err := store.PutV3EnvironmentWatch(existing); err != nil {
			return "", err
		}
	}
	r.followEnvironmentWatch(existing)
	where := ""
	if machine != "" {
		where = " on " + machine
	}
	result["status"] = EnvelopeStateWatching
	result["watch_id"] = id
	result["build_status"] = outcome.Status
	if outcome.Progress != nil {
		result["progress"] = outcome.Progress
	}
	result["notice"] = "Watching the environment build of " + path + where + ". Reply to the person now and end your turn; a new turn starts with the outcome when the build ends."
	result["next_action"] = EnvironmentWatchNextAction
	out, err := json.Marshal(result)
	return string(out), err
}

// checkLinkedEnvironmentMachine refuses a machine that is not linked or whose
// linked work rule is off.
func (r *Runtime) checkLinkedEnvironmentMachine(ctx context.Context, scope WorkspaceScope, machine string) error {
	fleetService, ok := r.getProjectTaskLifecycleService().(FleetProjects)
	if !ok {
		return errors.New("fleet service unavailable")
	}
	machines, err := fleetService.ListFleetMachines(ctx, scope.Principal)
	if err != nil {
		return err
	}
	access, err := r.sessions.GetAccountPermissions(scope.Principal.AccountScopeID)
	if err != nil {
		return err
	}
	linked := false
	for i := range machines {
		machines[i].Access = access.Link(machines[i].Name).Work
		if machines[i].Name == machine {
			linked = true
		}
	}
	if !linked || !pebblestore.LinkedWorkOn(access.Link(machine).Work) {
		return FleetMachineUnavailableError(machine, machines, linked)
	}
	return nil
}

// cancelEnvironmentWatches ends this conversation's watches on the folder
// (on machine), or the one named by watch_id.
func (r *Runtime) cancelEnvironmentWatches(scope WorkspaceScope, args map[string]any) (string, error) {
	store, err := r.environmentWatchStore()
	if err != nil {
		return "", err
	}
	machine := strings.TrimSpace(asString(args["machine"]))
	path := strings.TrimSpace(asString(args["workspace_path"]))
	watchID := strings.TrimSpace(asString(args["watch_id"]))
	watches, err := store.ListV3EnvironmentWatches(scope.SessionID)
	if err != nil {
		return "", err
	}
	var cancelled []string
	for _, watch := range watches {
		if watchID != "" && watch.ID != watchID {
			continue
		}
		if watchID == "" && (watch.WorkspacePath != path || watch.Machine != machine) {
			continue
		}
		if err := store.DeleteV3EnvironmentWatch(watch.SessionID, watch.ID); err != nil {
			return "", err
		}
		r.stopFollowingEnvironmentWatch(watch.ID)
		cancelled = append(cancelled, watch.ID)
	}
	if len(cancelled) == 0 {
		return "", errors.New("no environment watch on this conversation matches; nothing was cancelled")
	}
	result := map[string]any{"tool": "manage_projects", "action": "cancel_watch", "status": EnvelopeStateCancelled, "watch_ids": cancelled}
	if path != "" {
		result["workspace_path"] = path
	}
	if machine != "" {
		result["machine"] = machine
	}
	out, err := json.Marshal(result)
	return string(out), err
}

// followEnvironmentWatch follows a recorded watch in the background until
// its build ends or the watch is cancelled. A watch already followed here is
// not followed twice.
func (r *Runtime) followEnvironmentWatch(watch pebblestore.V3EnvironmentWatchRecord) {
	r.envWatches.mu.Lock()
	if r.envWatches.following == nil {
		r.envWatches.following = map[string]*environmentFollow{}
	}
	if _, ok := r.envWatches.following[watch.ID]; ok {
		r.envWatches.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	follow := &environmentFollow{cancel: cancel}
	r.envWatches.following[watch.ID] = follow
	r.envWatches.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			r.envWatches.mu.Lock()
			if r.envWatches.following[watch.ID] == follow {
				delete(r.envWatches.following, watch.ID)
			}
			r.envWatches.mu.Unlock()
		}()
		r.runEnvironmentWatch(ctx, watch)
	}()
}

func (r *Runtime) stopFollowingEnvironmentWatch(id string) {
	r.envWatches.mu.Lock()
	defer r.envWatches.mu.Unlock()
	if follow, ok := r.envWatches.following[id]; ok {
		follow.cancel()
		delete(r.envWatches.following, id)
	}
}

func (r *Runtime) stopEnvironmentWatches() {
	r.envWatches.mu.Lock()
	defer r.envWatches.mu.Unlock()
	for id, follow := range r.envWatches.following {
		follow.cancel()
		delete(r.envWatches.following, id)
	}
}

func (r *Runtime) runEnvironmentWatch(ctx context.Context, watch pebblestore.V3EnvironmentWatchRecord) {
	watcher, err := r.environmentWatcher()
	if err != nil {
		log.Printf("swarmd environment watch %s: %v", watch.ID, err)
		return
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: watch.UserID, AccountScopeID: watch.AccountScopeID}
	raw, awaitErr := watcher.AwaitFolderEnvironmentBuild(ctx, principal, watch.Machine, watch.WorkspacePath)
	if ctx.Err() != nil {
		return // cancelled, or the daemon is stopping: the record stays for a restart
	}
	store, err := r.environmentWatchStore()
	if err != nil {
		log.Printf("swarmd environment watch %s: %v", watch.ID, err)
		return
	}
	if current, found, err := store.GetV3EnvironmentWatch(watch.SessionID, watch.ID); err != nil || !found || current.CreatedAt != watch.CreatedAt {
		return // cancelled (or replaced) while the build ran
	}
	outcome := environmentOutcome{}
	if awaitErr == nil {
		outcome = environmentOutcomeFrom(raw)
	}
	content := environmentWatchMessage(watch, outcome, awaitErr)
	metadata := map[string]any{"environment_watch_id": watch.ID, "workspace_path": watch.WorkspacePath, "untrusted_content": true}
	if watch.Machine != "" {
		metadata["machine"] = watch.Machine
	}
	if awaitErr != nil {
		metadata["environment_status"] = "unknown"
	} else {
		metadata["environment_status"] = outcome.Status
	}
	// One delivery per watched build outcome: the wake id names the watch,
	// its registration and the build that ended.
	wakeID := fmt.Sprintf("%s:%d:%s:%d", watch.ID, watch.CreatedAt, outcome.Hash, outcome.FinishedAt)
	_, err = r.deliverSessionWake(principal, watch.SessionID, "environment-watch:"+wakeID, &pebblestore.MessageSnapshot{Role: "user", Content: content, Metadata: metadata}, &pebblestore.V3SessionWakeMutation{ID: wakeID, Source: pebblestore.SessionWakeSourceEnvironmentWatch})
	if err != nil && !errors.Is(err, pebblestore.ErrProjectTaskWaitStale) {
		// Kept for the next start: the outcome is not dropped.
		log.Printf("swarmd environment watch %s delivery: %v", watch.ID, err)
		return
	}
	if err := store.DeleteV3EnvironmentWatch(watch.SessionID, watch.ID); err != nil {
		log.Printf("swarmd environment watch %s record: %v", watch.ID, err)
	}
	if r.envWatches.delivered != nil {
		r.envWatches.delivered(watch.ID)
	}
}

// ResumeEnvironmentWatches follows again, at startup, the environment
// watches recorded before a restart (a build that a restart interrupted is
// failed, and that outcome is delivered).
func (r *Runtime) ResumeEnvironmentWatches() {
	store, err := r.environmentWatchStore()
	if err != nil {
		return
	}
	watches, err := store.ListV3EnvironmentWatches("")
	if err != nil {
		log.Printf("swarmd environment watch recovery: %v", err)
		return
	}
	for _, watch := range watches {
		r.followEnvironmentWatch(watch)
	}
}

// environmentOutcome is what a get_environment answer says about a build.
type environmentOutcome struct {
	Defined     bool
	Status      string
	Hash        string
	Error       string
	RunsOn      string
	Image       string
	StartedAt   int64
	FinishedAt  int64
	DurationMs  int64
	Log         string
	LogComplete bool
	Progress    map[string]any
}

func environmentOutcomeFrom(raw json.RawMessage) environmentOutcome {
	var answer struct {
		Environment struct {
			Defined   bool           `json:"defined"`
			Status    string         `json:"status"`
			Hash      string         `json:"hash"`
			Error     string         `json:"error"`
			RunsOn    string         `json:"runs_on"`
			Log       string         `json:"log"`
			Progress  map[string]any `json:"progress"`
			LastBuild struct {
				StartedAt  int64  `json:"started_at"`
				FinishedAt int64  `json:"finished_at"`
				DurationMs int64  `json:"duration_ms"`
				Image      string `json:"image"`
			} `json:"last_build"`
			LogPage struct {
				Offset       int `json:"offset"`
				DroppedBytes int `json:"dropped_bytes"`
				TotalBytes   int `json:"total_bytes"`
			} `json:"log_page"`
		} `json:"environment"`
	}
	if json.Unmarshal(raw, &answer) != nil {
		return environmentOutcome{}
	}
	env := answer.Environment
	return environmentOutcome{
		Defined: env.Defined, Status: env.Status, Hash: env.Hash, Error: env.Error, RunsOn: env.RunsOn,
		Image: env.LastBuild.Image, StartedAt: env.LastBuild.StartedAt, FinishedAt: env.LastBuild.FinishedAt, DurationMs: env.LastBuild.DurationMs,
		Log: env.Log, LogComplete: env.LogPage.Offset == 0 && env.LogPage.DroppedBytes == 0, Progress: env.Progress,
	}
}

// logTail is the log's last n lines and whether earlier lines were left out.
func (o environmentOutcome) logTail(n int) (string, bool) {
	text := strings.TrimRight(o.Log, "\n")
	if text == "" {
		return "", !o.LogComplete
	}
	lines := strings.Split(text, "\n")
	cut := !o.LogComplete
	if len(lines) > n {
		lines, cut = lines[len(lines)-n:], true
	}
	return strings.Join(lines, "\n"), cut
}

func (o environmentOutcome) view(lines int) map[string]any {
	view := map[string]any{"status": o.Status}
	if o.Hash != "" {
		view["hash"] = o.Hash
	}
	if o.Image != "" {
		view["image"] = o.Image
	}
	if o.DurationMs > 0 {
		view["duration_ms"] = o.DurationMs
	}
	if o.FinishedAt > 0 {
		view["finished_at"] = o.FinishedAt
	}
	if o.Error != "" {
		view["error"] = o.Error
	}
	if o.RunsOn != "" {
		view["runs_on"] = o.RunsOn
	}
	if tail, cut := o.logTail(lines); tail != "" {
		view["log_tail"] = tail
		view["log_tail_cut"] = cut
	}
	return view
}

// environmentWatchMessage is the outcome a watch delivers. The log is the
// setup commands' output: untrusted text.
func environmentWatchMessage(watch pebblestore.V3EnvironmentWatchRecord, outcome environmentOutcome, awaitErr error) string {
	where := ""
	if watch.Machine != "" {
		where = " on linked machine " + watch.Machine
	}
	var b strings.Builder
	if awaitErr != nil {
		fmt.Fprintf(&b, "[Environment watch ended without an outcome]\nFolder: %s%s\nReason: %s\n", watch.WorkspacePath, where, truncateBackgroundText(awaitErr.Error(), 600))
		b.WriteString("Read the folder's environment with manage_projects get_environment (include_log) to see where its build is.")
		return b.String()
	}
	fmt.Fprintf(&b, "[Environment build ended: %s]\nFolder: %s%s\n", outcome.Status, watch.WorkspacePath, where)
	if outcome.DurationMs > 0 {
		fmt.Fprintf(&b, "Duration: %s\n", (time.Duration(outcome.DurationMs) * time.Millisecond).Round(time.Second))
	}
	if outcome.Image != "" {
		fmt.Fprintf(&b, "Image: %s\n", outcome.Image)
	}
	if outcome.Hash != "" {
		fmt.Fprintf(&b, "Definition: %s\n", outcome.Hash)
	}
	if outcome.Error != "" {
		fmt.Fprintf(&b, "Error: %s\n", outcome.Error)
	}
	if outcome.RunsOn != "" {
		fmt.Fprintf(&b, "The folder runs on %s.\n", outcome.RunsOn)
	}
	tail, cut := outcome.logTail(EnvironmentWatchLogLines)
	switch {
	case tail == "":
		b.WriteString("The build wrote no log.\n")
	case cut:
		fmt.Fprintf(&b, "Last %d lines of the build log (untrusted output of the setup commands; get_environment include_log with log_offset pages the whole log):\n", EnvironmentWatchLogLines)
	default:
		b.WriteString("Build log (untrusted output of the setup commands):\n")
	}
	if tail != "" {
		b.WriteString("```\n" + tail + "\n```")
	}
	return strings.TrimRight(b.String(), "\n")
}
