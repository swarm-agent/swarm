package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/design"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Design jobs make standalone HTML designs (or edit one HTML file) in a
// project folder with the daemon's design pipeline: one model call per
// design, a browser check, at most one repair call. A job is a project task
// with a Design record and a hidden receipt session that carries its usage.
// It has no agent run, worktree or branch: the files land in the folder and
// there is nothing to merge.

// DesignModelFactory resolves the account's Designer model for one job;
// *run.Service implements it.
type DesignModelFactory interface {
	DesignModel(p identity.Principal, receiptSessionID, jobID string) (design.Model, error)
}

type designJobRuntime struct {
	checker design.Checker
	models  DesignModelFactory
	mu      sync.Mutex
	running map[string]*designJobHandle
}

type designJobHandle struct {
	cancel  context.CancelFunc
	stopped bool
}

// SetDesignJobs installs the browser checker and the Designer model source
// design jobs use. Without them the design-jobs route refuses.
func (s *Server) SetDesignJobs(checker design.Checker, models DesignModelFactory) {
	if checker == nil || models == nil {
		s.designJobs = nil
		return
	}
	s.designJobs = &designJobRuntime{checker: checker, models: models, running: map[string]*designJobHandle{}}
}

func designJobKey(account, project, task string) string {
	return account + "\x00" + project + "\x00" + task
}

// designJobCreateRequest is the body of POST /v3/projects/{id}/design-jobs.
type designJobCreateRequest struct {
	Workspace  string   `json:"workspace"`
	Brief      string   `json:"brief"`
	Count      int      `json:"count"`
	Style      string   `json:"style"`
	Variations []string `json:"variations"`
	BatchName  string   `json:"batch_name"`
	Folder     string   `json:"folder"`
	EditPath   string   `json:"edit_path"`
}

const maxDesignJobBodyBytes = 1 << 20

// normalizeDesignJobRequest validates the request before anything is read
// or spent. The returned request has defaults applied; Folder is the parent
// folder (the job's own folder is added once the task id exists).
func normalizeDesignJobRequest(in designJobCreateRequest) (designJobCreateRequest, design.Request, error) {
	in.Brief = strings.TrimSpace(in.Brief)
	in.Style = strings.TrimSpace(in.Style)
	in.BatchName = strings.TrimSpace(in.BatchName)
	// Only a trailing slash is dropped: a leading one (an absolute path) is
	// refused below, and a folder of only slashes is not a folder.
	if folder := strings.TrimSpace(in.Folder); folder != "" {
		if in.Folder = strings.TrimRight(folder, "/"); in.Folder == "" {
			return in, design.Request{}, errors.New("folder must be a relative path inside the workspace")
		}
	}
	in.EditPath = strings.TrimSpace(in.EditPath)
	if utf8.RuneCountInString(in.BatchName) > 200 {
		return in, design.Request{}, errors.New("batch_name is longer than 200 characters")
	}
	if in.EditPath != "" {
		if in.Folder != "" || in.BatchName != "" || len(in.Variations) > 0 {
			return in, design.Request{}, errors.New("an edit changes edit_path in place: folder, batch_name and variations do not apply")
		}
		if in.Count == 0 {
			in.Count = 1
		}
		if !pebblestore.ValidDesignRelativePath(in.EditPath) {
			return in, design.Request{}, errors.New("edit_path must be a relative path inside the workspace (no '..', no absolute path)")
		}
	} else {
		if in.Count == 0 {
			in.Count = 1
		}
		if in.Folder == "" {
			in.Folder = design.DefaultFolder
		}
		if !pebblestore.ValidDesignRelativePath(in.Folder) {
			return in, design.Request{}, errors.New("folder must be a relative path inside the workspace (no '..', no absolute path)")
		}
	}
	req := design.Request{Brief: in.Brief, Count: in.Count, Style: in.Style, Variations: in.Variations, Folder: in.Folder, EditPath: in.EditPath}
	if err := design.Validate(req); err != nil {
		return in, design.Request{}, err
	}
	return in, req, nil
}

// designJobSlug names a batch folder from the batch name or the brief.
func designJobSlug(name string) string { return design.BatchSlug(name) }

func newDesignTaskID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "task_" + hex.EncodeToString(b), nil
}

// handleProjectDesignJobs serves POST /v3/projects/{id}/design-jobs and
// POST /v3/projects/{id}/design-jobs/{task}/cancel.
func (s *Server) handleProjectDesignJobs(w http.ResponseWriter, r *http.Request, p identity.Principal, projectID string, rest []string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
		return
	}
	switch {
	case len(rest) == 0:
		s.createDesignJob(w, r, p, projectID)
	case len(rest) == 2 && rest[1] == "cancel":
		s.cancelDesignJob(w, p, projectID, rest[0])
	default:
		writeError(w, http.StatusNotFound, errors.New("unknown design job route"))
	}
}

func (s *Server) createDesignJob(w http.ResponseWriter, r *http.Request, p identity.Principal, projectID string) {
	jobs := s.designJobs
	if jobs == nil || s.sessions == nil || s.sessions.Store() == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("design jobs are not available on this daemon"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxDesignJobBodyBytes+1))
	if err != nil || len(body) > maxDesignJobBodyBytes {
		writeError(w, http.StatusBadRequest, errors.New("request body is unreadable or larger than 1 MiB"))
		return
	}
	var in designJobCreateRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid design job request: %w", err))
		return
	}
	in, req, err := normalizeDesignJobRequest(in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	db := s.sessions.Store()
	proj, found, err := db.GetProject(p.AccountScopeID, projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found || proj == nil {
		writeError(w, http.StatusNotFound, errors.New("project not found"))
		return
	}
	workspacePath := strings.TrimSpace(in.Workspace)
	if workspacePath != "" {
		if !filepath.IsAbs(workspacePath) {
			writeError(w, http.StatusBadRequest, errors.New("workspace must be an absolute folder path of the project"))
			return
		}
		workspacePath = filepath.Clean(workspacePath)
	}
	source, err := s.resolveProjectTaskSource(p, proj, workspacePath, "", 0, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	taskID, err := newDesignTaskID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	resultsPath := ""
	if req.Mode() == design.ModeCreate {
		name := in.BatchName
		if name == "" {
			name = in.Brief
		}
		req.Folder = path.Join(in.Folder, designJobSlug(name)+"-"+taskID[len(taskID)-6:])
		resultsPath = path.Join(req.Folder, design.ResultsFile)
	}
	files, err := tool.NewDesignFiles(source.Path, resultsPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Mode() == design.ModeEdit {
		if _, _, err := files.Read(r.Context(), req.EditPath); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("edit_path must be an existing HTML file inside the workspace: %w", err))
			return
		}
	} else if err := files.CheckFolder(r.Context(), req.Folder); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sessionID := sessionruntime.NewSessionID()
	model, err := jobs.models.DesignModel(p, sessionID, taskID)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	info := model.Info()
	now := time.Now().UnixMilli()
	title := in.BatchName
	if title == "" {
		title = truncateString(strings.SplitN(in.Brief, "\n", 2)[0], 80)
	}
	if req.Mode() == design.ModeEdit {
		title = "Edit " + req.EditPath
	} else {
		title = "Designs: " + title
	}
	if err := s.createDesignReceiptSession(p, proj, source, sessionID, taskID, title, info, now); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("create the design job's usage receipt session: %w", err))
		return
	}
	job := &pebblestore.ProjectTaskDesignJob{
		Mode: string(req.Mode()), Brief: req.Brief, Count: req.Count, Style: req.Style,
		Variations: append([]string(nil), req.Variations...), BatchName: in.BatchName,
		Folder: req.Folder, ResultsPath: resultsPath, EditPath: req.EditPath, State: pebblestore.DesignJobRunning,
		Provider: info.Provider, Model: info.Model, Thinking: info.Thinking, StartedAt: now,
	}
	action := fmt.Sprintf("Making %d design(s) in %s", req.Count, req.Folder)
	if req.Mode() == design.ModeEdit {
		action = "Editing " + req.EditPath
	}
	task := &pebblestore.ProjectTaskRecord{
		ID: taskID, ProjectID: proj.ID, AccountID: p.AccountScopeID, Title: title, Description: req.Brief,
		Status: "in_progress", SessionID: sessionID, Agent: "designer", OutcomeType: "ui_design", Tier: "direct",
		WorkspacePath: source.Path, SourceWorkspace: source, Provider: info.Provider, Model: info.Model, Thinking: info.Thinking,
		ActionNeeded: action, Design: job, CreatedAt: now,
	}
	reserved, err := db.ReserveProjectTaskIfAbsent(p.AccountScopeID, task)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !reserved {
		writeError(w, http.StatusConflict, errors.New("design job id collision; retry"))
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	handle := &designJobHandle{cancel: cancel}
	key := designJobKey(p.AccountScopeID, proj.ID, taskID)
	jobs.mu.Lock()
	jobs.running[key] = handle
	jobs.mu.Unlock()
	go s.executeDesignJob(ctx, p, proj.ID, taskID, req, model, files, jobs, key, handle)
	writeJSON(w, http.StatusAccepted, map[string]any{"task": sanitizeProjectTaskForClient(task)})
}

// createDesignReceiptSession makes the hidden V3 session that carries the
// job's usage receipts, so usage summaries and cost views see the job.
func (s *Server) createDesignReceiptSession(p identity.Principal, proj *pebblestore.ProjectRecord, source pebblestore.ProjectTaskSource, sessionID, taskID, title string, info design.ModelInfo, now int64) error {
	snapshot := pebblestore.SessionSnapshot{
		ID: sessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID,
		WorkspacePath: source.Path, WorkspaceName: filepath.Base(source.Path),
		Title: fmt.Sprintf("[%s] %s", proj.Name, title), Mode: sessionruntime.ModeAuto,
		Preference: pebblestore.ModelPreference{Provider: info.Provider, Model: info.Model, Thinking: info.Thinking},
		Metadata: map[string]any{
			"system_session": true, "navigation_hidden": true, "settings_locked": true,
			"source": "design_job", "role": "design_job_receipt",
			"project_id": proj.ID, "task_id": taskID, "workspace_id": source.WorkspaceID,
		},
		CreatedAt: now, UpdatedAt: now,
	}
	key := "design-job:create:" + proj.ID + ":" + taskID + ":" + sessionID
	hash := sha256.Sum256([]byte(key))
	_, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: sessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID,
		ClientRequestID: key, IdempotencyKey: key, PayloadHash: hex.EncodeToString(hash[:]), RequestHash: hex.EncodeToString(hash[:]),
		Kind: sessionruntime.SessionMutationCreateSession, Session: &snapshot, NowUnixMs: now,
	})
	return err
}

// designJobProgress writes each finished design to the job record. Batch
// snapshots can arrive out of order; an older one never replaces a newer one.
type designJobProgress struct {
	s                      *Server
	account, project, task string
	mu                     sync.Mutex
	applied                int
}

func (g *designJobProgress) Item(design.ItemResult) {}

func (g *designJobProgress) Batch(batch design.BatchResult) {
	g.mu.Lock()
	defer g.mu.Unlock()
	finished := 0
	for _, item := range batch.Items {
		if item.Status != "" {
			finished++
		}
	}
	if finished < g.applied || (finished == g.applied && batch.FinishedAt == 0) {
		return
	}
	g.applied = finished
	if _, err := g.s.updateDesignJob(g.account, g.project, g.task, func(t *pebblestore.ProjectTaskRecord) error {
		if t.Design == nil || t.Design.State != pebblestore.DesignJobRunning {
			return errors.New("design job is no longer running")
		}
		applyDesignBatch(t.Design, batch)
		t.ActionNeeded = fmt.Sprintf("%d of %d design(s) finished", finished, t.Design.Count)
		return nil
	}); err != nil {
		log.Printf("design job %s progress not recorded: %v", g.task, err)
	}
}

func (s *Server) updateDesignJob(account, project, task string, mutate func(*pebblestore.ProjectTaskRecord) error) (*pebblestore.ProjectTaskRecord, error) {
	return s.sessions.Store().UpdateProjectTask(account, project, task, func(t *pebblestore.ProjectTaskRecord) error {
		if err := mutate(t); err != nil {
			return err
		}
		t.Revision++
		return nil
	})
}

func (s *Server) executeDesignJob(ctx context.Context, p identity.Principal, projectID, taskID string, req design.Request, model design.Model, files design.Files, jobs *designJobRuntime, key string, handle *designJobHandle) {
	defer func() {
		jobs.mu.Lock()
		delete(jobs.running, key)
		jobs.mu.Unlock()
		handle.cancel()
	}()
	progress := &designJobProgress{s: s, account: p.AccountScopeID, project: projectID, task: taskID}
	pipeline := design.Pipeline{Model: model, Checker: jobs.checker, Files: files, Progress: progress, Concurrency: design.DefaultConcurrency}
	var (
		result design.BatchResult
		runErr error
	)
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				runErr = fmt.Errorf("design pipeline stopped unexpectedly: %v", recovered)
			}
		}()
		result, runErr = pipeline.Run(ctx, req)
	}()
	jobs.mu.Lock()
	stopped := handle.stopped
	jobs.mu.Unlock()
	if _, err := s.updateDesignJob(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
		if t.Design == nil || t.Design.State != pebblestore.DesignJobRunning {
			return errors.New("design job is no longer running")
		}
		finishDesignJob(t, result, runErr, stopped, time.Now().UnixMilli())
		return nil
	}); err != nil {
		log.Printf("design job %s outcome not recorded: %v", taskID, err)
	}
}

// finishDesignJob settles the record: cancelled when stopped, completed when
// at least one design was written (create) or the file was replaced (edit),
// failed otherwise.
func finishDesignJob(t *pebblestore.ProjectTaskRecord, result design.BatchResult, runErr error, stopped bool, now int64) {
	job := t.Design
	if runErr == nil {
		applyDesignBatch(job, result)
	}
	written := 0
	for _, item := range job.Items {
		if (job.Mode == string(design.ModeCreate) && item.Path != "") || (job.Mode == string(design.ModeEdit) && item.SHA256 != "") {
			written++
		}
	}
	job.FinishedAt = max(now, job.StartedAt)
	switch {
	case stopped:
		job.State, job.Error = pebblestore.DesignJobCancelled, fmt.Sprintf("stopped; %d/%d finished", job.Finished(), job.Count)
	case runErr != nil:
		job.State, job.Error = pebblestore.DesignJobFailed, truncateRunes(runErr.Error(), 1000)
	case written > 0:
		job.State, job.Error = pebblestore.DesignJobCompleted, ""
	default:
		job.State = pebblestore.DesignJobFailed
		job.Error = "no design was written: " + designCountsText(job.Counts)
		if job.Mode == string(design.ModeEdit) {
			job.Error = "the file was not changed: " + designCountsText(job.Counts)
			for _, item := range job.Items {
				if item.Error != "" {
					job.Error += "; " + item.Error
				}
			}
		}
		job.Error = truncateRunes(job.Error, 1000)
	}
	t.Status, t.LastError = job.State, job.Error
	switch job.State {
	case pebblestore.DesignJobCompleted:
		if job.Mode == string(design.ModeEdit) {
			t.ActionNeeded = job.EditPath + " was edited in place; nothing to merge."
		} else {
			t.ActionNeeded = fmt.Sprintf("%d of %d design(s) written to %s; nothing to merge.", written, job.Count, job.Folder)
		}
	default:
		t.ActionNeeded = "The design job did not finish: " + job.Error
	}
}

func designCountsText(counts map[string]int) string {
	parts := []string{}
	for _, status := range []design.Status{design.StatusPassed, design.StatusRepaired, design.StatusFailed, design.StatusCheckUnavailable, design.StatusGenerationFailed, design.StatusSkippedBudget, design.StatusCancelled, design.StatusStaleBase, design.StatusEditRefused} {
		if n := counts[string(status)]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, status))
		}
	}
	if len(parts) == 0 {
		return "no design finished"
	}
	return strings.Join(parts, ", ")
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max-1]) + "…"
}

func designDiagnostics(report *design.CheckReport) (string, []pebblestore.ProjectTaskDesignDiagnostic) {
	if report == nil {
		return "", nil
	}
	var out []pebblestore.ProjectTaskDesignDiagnostic
	for _, d := range report.Diagnostics {
		if len(out) == 3 {
			break
		}
		kind := d.Kind
		if len(kind) > 64 {
			kind = kind[:64]
		}
		out = append(out, pebblestore.ProjectTaskDesignDiagnostic{Kind: kind, Message: truncateRunes(d.Message, 200)})
	}
	status := string(report.Status)
	if report.Status == design.CheckUnavailable && report.Code != "" {
		status += ": " + truncateRunes(report.Code, 100)
	}
	return status, out
}

// applyDesignBatch copies a pipeline result into the bounded job record.
func applyDesignBatch(job *pebblestore.ProjectTaskDesignJob, batch design.BatchResult) {
	job.Provider, job.Model, job.Thinking = batch.Model.Provider, batch.Model.Model, batch.Model.Thinking
	job.Items = job.Items[:0]
	for _, item := range batch.Items {
		if item.Status == "" {
			continue
		}
		out := pebblestore.ProjectTaskDesignItem{
			N: item.Index, Path: item.Path, SHA256: item.SHA256, Status: string(item.Status),
			Error: truncateRunes(item.Error, 400), Variation: item.Variation, Calls: len(item.Calls), DurationMS: item.DurationMS,
			UsageComplete: true,
		}
		out.CheckBefore, out.Before = designDiagnostics(item.CheckBefore)
		out.CheckAfter, out.After = designDiagnostics(item.CheckAfter)
		if item.Repair != nil {
			out.RepairEdits, out.RepairRefused = item.Repair.Edits, truncateRunes(item.Repair.Refused, 400)
		}
		var cost float64
		priced := false
		for _, call := range item.Calls {
			out.Tokens += call.Total
			if call.CostUSD != nil {
				cost += *call.CostUSD
				priced = true
			}
			if !call.Reported || call.CostUSD == nil {
				out.UsageComplete = false
			}
		}
		if priced {
			out.CostUSD = &cost
		}
		job.Items = append(job.Items, out)
	}
	job.Counts = map[string]int{}
	for status, n := range batch.Counts {
		job.Counts[string(status)] = n
	}
	job.Calls, job.Tokens, job.CostUSD, job.CostComplete = batch.Totals.Calls, batch.Totals.Tokens, batch.Totals.CostUSD, batch.Totals.CostComplete
	job.EditBaseSHA256, job.EditFinalSHA256 = batch.EditBaseSHA256, batch.EditFinalSHA256
	job.ResultsError = truncateRunes(batch.ResultsError, 1000)
}

func (s *Server) cancelDesignJob(w http.ResponseWriter, p identity.Principal, projectID, taskID string) {
	if s.sessions == nil || s.sessions.Store() == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("session service unavailable"))
		return
	}
	task, found, err := s.sessions.Store().GetProjectTask(p.AccountScopeID, projectID, taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found || task == nil || task.Design == nil {
		writeError(w, http.StatusNotFound, errors.New("design job not found"))
		return
	}
	if task.Design.State != pebblestore.DesignJobRunning {
		writeError(w, http.StatusConflict, fmt.Errorf("the design job already ended (%s)", task.Design.State))
		return
	}
	jobs := s.designJobs
	var handle *designJobHandle
	if jobs != nil {
		jobs.mu.Lock()
		handle = jobs.running[designJobKey(p.AccountScopeID, projectID, taskID)]
		if handle != nil {
			handle.stopped = true
		}
		jobs.mu.Unlock()
	}
	if handle == nil {
		writeError(w, http.StatusConflict, errors.New("the design job is not running in this daemon"))
		return
	}
	handle.cancel()
	writeJSON(w, http.StatusAccepted, map[string]any{"task_id": taskID, "status": "cancelling"})
}

// refuseDesignTaskMutation answers task routes that would act on a design
// job as if it were an agent task (approve, reopen, refine, programs, edits,
// integration). A running job only reads and cancels; a settled one may also
// be archived or deleted.
func (s *Server) refuseDesignTaskMutation(w http.ResponseWriter, r *http.Request, p identity.Principal, projectID string, segments []string) bool {
	if len(segments) < 3 || segments[1] != "tasks" || r.Method == http.MethodGet || r.Method == http.MethodHead {
		return false
	}
	task, found, err := s.sessions.Store().GetProjectTask(p.AccountScopeID, projectID, segments[2])
	if err != nil || !found || task == nil || task.Design == nil {
		return false
	}
	op := ""
	if len(segments) > 3 {
		op = segments[3]
	}
	settled := task.Design.State != pebblestore.DesignJobRunning
	if settled && ((len(segments) == 3 && r.Method == http.MethodDelete) || (len(segments) == 4 && (op == "archive" || op == "unarchive"))) {
		return false
	}
	message := "this task is a design job: its designs are files in the project folder, with nothing to approve, refine or merge"
	if !settled {
		message += "; stop it with POST " + ProjectsPath + "/" + projectID + "/design-jobs/" + task.ID + "/cancel"
	}
	writeError(w, http.StatusConflict, errors.New(message))
	return true
}

// errDesignJobNotAgentTask refuses agent-task lifecycle operations (approve,
// deploy) on a design job.
var errDesignJobNotAgentTask = errors.New("this task is a design job: it runs the design pipeline, not an agent; there is nothing to approve or deploy")
