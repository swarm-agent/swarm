package api

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// ManageMedia is the in-process session bridge to canonical media tasks.
func (s *Server) ManageMedia(ctx context.Context, scope tool.WorkspaceScope, args map[string]any) (any, error) {
	p := scope.Principal
	if !p.Valid() || s.sessions == nil {
		return nil, errors.New("media requires an authenticated session")
	}
	session, found, err := s.sessions.GetSession(scope.SessionID)
	if err != nil {
		return nil, err
	}
	if !found || session.AccountScopeID != p.AccountScopeID || session.UserID != p.UserID {
		return nil, errors.New("media session ownership mismatch")
	}
	action := controlMCPString(args, "action")
	if action == "models" {
		return s.mediaModels(ctx, p, args)
	}
	projectID, _ := session.Metadata["project_id"].(string)
	if projectID == "" {
		projectID = pebblestore.ProjectConversationID(session)
	}
	if projectID == "" {
		return nil, errors.New("manage_media requires a session with a project")
	}
	load := func(project, id string) (*pebblestore.ProjectTaskRecord, error) {
		if project != projectID {
			return nil, errors.New("media job belongs to another project")
		}
		task, found, err := s.sessions.Store().GetProjectTask(p.AccountScopeID, project, id)
		if err != nil {
			return nil, err
		}
		if !found || task == nil || !isDirectMediaTask(task) {
			return nil, errors.New("media job not found")
		}
		return task, nil
	}
	root := scope.PrimaryPath
	switch action {
	case "generate":
		kind := controlMCPMediaKinds[controlMCPString(args, "kind")]
		brief := strings.TrimSpace(controlMCPString(args, "brief"))
		if kind == "" || brief == "" {
			return nil, errors.New("generate requires kind image, video or sound and a brief")
		}
		// Restrict paths to the execution workspace, including resolved symlinks.
		for _, raw := range controlMCPList(args["inputs"]) {
			input := controlMCPMap(raw)
			path, _ := input["path"].(string)
			if path == "" {
				continue
			}
			if root == "" {
				return nil, errors.New("media inputs require a session workspace")
			}
			if !filepath.IsAbs(path) {
				path = filepath.Join(root, path)
			}
			if err := tool.ValidateMediaWorkspacePath(scope, path, false); err != nil {
				return nil, err
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return nil, err
			}
			resolvedRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				return nil, err
			}
			if controlMCPWorkspaceRootOf([]string{resolvedRoot}, resolved) == "" {
				return nil, errors.New("media input leaves session workspace")
			}
			input["path"] = path
		}
		inputs, err := mediaInputs(args["inputs"], func() ([]string, error) { return []string{root}, nil }, load)
		if err != nil {
			return nil, err
		}
		input := tool.ProjectTaskCreateInput{OriginSessionID: session.ID, Title: brief, Prompt: brief, Description: brief, Agent: kind, WorkspacePath: root, AutoApprove: true, Model: controlMCPString(args, "model"), AspectRatio: controlMCPString(args, "aspect_ratio"), Resolution: controlMCPString(args, "resolution"), DurationSeconds: controlMCPInt(args, "duration_seconds", 0), VariantCount: controlMCPInt(args, "count", 0)}
		if kind == "video" {
			input.VideoType = "single"
			input.Operation = controlMCPString(args, "operation")
		}
		raw, err := json.Marshal(inputs)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &input.AttachedMedia); err != nil {
			return nil, err
		}
		operation := uuid.NewString()
		if err := s.sessions.Store().CheckWorkerSessionBudgetWithPrice(p.AccountScopeID, session.ID, "unknown", operation); err != nil {
			return nil, err
		}
		task, createErr := s.CreateProjectTask(ctx, p, projectID, input)
		releaseErr := s.sessions.Store().ReleaseWorkerBudgetReservation(p.AccountScopeID, session.ID, operation)
		if err := errors.Join(createErr, releaseErr); err != nil {
			return nil, err
		}
		return map[string]any{"job_id": controlMCPJobID(controlMCPJobMedia, projectID, task.ID), "status": task.Status}, nil
	case "get", "save":
		kind, project, id, err := controlMCPParseJobID(args)
		if err != nil {
			return nil, err
		}
		if kind != controlMCPJobMedia {
			return nil, errors.New("job_id must identify a media job")
		}
		task, err := load(project, id)
		if err != nil {
			return nil, err
		}
		if action == "save" {
			if root == "" {
				return nil, errors.New("save requires a session workspace")
			}
			dir, err := mediaSaveDir([]string{root}, root, controlMCPString(args, "folder"))
			if err != nil {
				return nil, err
			}
			if err := tool.ValidateMediaWorkspacePath(scope, dir, true); err != nil {
				return nil, err
			}
			paths, err := saveMediaFiles(task, mediaTaskFiles(task), dir)
			return map[string]any{"files": paths}, err
		}
		wait := controlMCPInt(args, "wait_seconds", 0)
		if wait < 0 || wait > 30 {
			return nil, errors.New("wait_seconds must be between 0 and 30")
		}
		deadline := time.NewTimer(time.Duration(wait) * time.Second)
		defer deadline.Stop()
		if wait > 0 {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
		waiting:
			for task.Status == "running" || task.Status == "in_progress" || task.Status == "queued" || task.Status == "pending_approval" {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-deadline.C:
					break waiting
				case <-ticker.C:
					task, err = load(project, id)
					if err != nil {
						return nil, err
					}
				}
			}
		}
		return mediaJobFileView(map[string]any{"job_id": controlMCPJobID(controlMCPJobMedia, project, id), "status": task.Status}, task, nil)
	default:
		return nil, errors.New("action must be models, generate, get or save")
	}
}
