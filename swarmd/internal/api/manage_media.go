package api

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegen"
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
		return s.mediaModels(ctx, p, args, manageMediaModelsGuidance, "%d more need a provider key; configure credentials before using manage_media models again")
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
		input := tool.ProjectTaskCreateInput{ManageMediaSource: true, OriginSessionID: session.ID, Title: brief, Prompt: brief, Description: brief, Agent: kind, WorkspacePath: root, AutoApprove: pebblestore.SessionAccessFromMetadata(session.Metadata).Tasks != pebblestore.SessionTasksAsk, Model: controlMCPString(args, "model"), AspectRatio: controlMCPString(args, "aspect_ratio"), Resolution: controlMCPString(args, "resolution"), DurationSeconds: controlMCPInt(args, "duration_seconds", 0), VariantCount: controlMCPInt(args, "count", 0)}
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
		input.ID = "task_" + uuid.NewString()
		estimate, err := s.estimateManagedMedia(ctx, p, &input)
		if err != nil {
			return nil, err
		}
		// Check current usage plus the estimate once without reserving allowance.
		// Background generation records actual usage when its receipts arrive.
		if err := s.sessions.Store().CheckWorkerMediaAdmission(p.AccountScopeID, session.ID, estimate); err != nil {
			return nil, err
		}
		task, err := s.CreateProjectTask(ctx, p, projectID, input)
		if err != nil {
			return nil, err
		}
		result := map[string]any{"job_id": controlMCPJobID(controlMCPJobMedia, projectID, task.ID), "status": task.Status}
		if task.Status == "pending_approval" {
			result["message"] = "The task waits for approval."
		}
		return result, nil
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
					return map[string]any{"job_id": controlMCPJobID(controlMCPJobMedia, project, id), "status": task.Status, "message": "still generating; wait cancelled", "wait_error": ctx.Err().Error()}, nil
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
		return mediaJobFileView(map[string]any{"job_id": controlMCPJobID(controlMCPJobMedia, project, id), "status": task.Status}, task, nil, manageMediaJobGuidance)
	default:
		return nil, errors.New("action must be models, generate, get or save")
	}
}

// Pin the selected model before admission so pricing and execution agree.
func (s *Server) estimateManagedMedia(ctx context.Context, p identity.Principal, input *tool.ProjectTaskCreateInput) (pebblestore.MediaCostEstimate, error) {
	if input.Model == "" {
		input.Model = s.defaultMediaModel(ctx, p, input.Agent)
	}
	opts := pebblestore.MediaCostEstimateOptions{Model: input.Model, Kind: input.Agent, Count: max(1, input.VariantCount), ImageSize: input.Resolution, Resolution: input.Resolution, AspectRatio: input.AspectRatio, DurationSeconds: input.DurationSeconds, IncludesAudio: input.Agent == "video"}
	if input.Agent == "image" && s.imageGen != nil {
		selection, err := s.imageGen.ResolveModelSelection(input.Model)
		if err != nil {
			return pebblestore.MediaCostEstimate{}, err
		}
		input.Model = selection.ID
		if defaults := s.getModelGenerationOptions(input.Model); defaults != nil {
			if input.Resolution == "" {
				input.Resolution = defaults.DefaultRes
				opts.ImageSize = input.Resolution
				opts.Resolution = input.Resolution
			}
			if input.AspectRatio == "" {
				input.AspectRatio = defaults.DefaultRatio
				opts.AspectRatio = input.AspectRatio
			}
		}
		opts.Provider, opts.Model = selection.Provider, selection.Model
	} else {
		catalog, err := s.mediaCatalogFor(ctx, p)
		if err != nil {
			return pebblestore.MediaCostEstimate{}, err
		}
		options := catalog.VideoGenerationModels
		if input.Agent == "sound" {
			options = catalog.AudioModels
			opts.Kind = "audio"
		}
		for _, option := range options {
			if option.ID == input.Model {
				opts.Provider = option.Provider
				break
			}
		}
	}
	if opts.Provider == imagegen.ProviderGoogleGemini {
		opts.Provider = "google"
	}
	if opts.Provider == imagegen.ProviderCodexOpenAI {
		opts.Provider = "codex"
	}
	return s.sessions.EstimateMediaCostWithOptions(opts), nil
}

const manageMediaModelsGuidance = "Use manage_media generate with a model and its supported options; manage_media models lists those options. Use manage_media get with wait_seconds to wait, then manage_media save to write files into the workspace."

func manageMediaJobGuidance(out map[string]any, _ []controlMCPMediaFile) {
	switch out["status"] {
	case "needs_review", "completed":
		out["next"] = "The files are ready; use manage_media save to write them into the workspace."
	case "failed", "blocked", "cancelled", "rejected":
		out["next"] = "Read each file's error, consult manage_media models for supported options, then use manage_media generate for a corrected new job."
	default:
		out["next"] = "Use manage_media get with wait_seconds to wait for the job, then manage_media save when its files are ready."
	}
}
