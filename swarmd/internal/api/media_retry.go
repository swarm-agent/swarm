package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// readableMediaTask is shared by internal media reads and retries. Older tasks
// without an origin remain account-owned; session-owned jobs also require the user.
func (s *Server) readableMediaTask(p identity.Principal, project, id string) (*pebblestore.ProjectTaskRecord, error) {
	task, found, err := s.sessions.Store().GetProjectTask(p.AccountScopeID, project, id)
	if err != nil {
		return nil, err
	}
	if !found || task == nil || task.AccountID != p.AccountScopeID || task.ProjectID != project || !isDirectMediaTask(task) {
		return nil, errors.New("media job not found")
	}
	if task.OriginSessionID != "" {
		origin, found, err := s.sessions.GetSession(task.OriginSessionID)
		if err != nil {
			return nil, err
		}
		if !found || origin.AccountScopeID != p.AccountScopeID || origin.UserID != p.UserID {
			return nil, errors.New("media job ownership mismatch")
		}
	}
	return task, nil
}

// retryMedia copies only generation inputs, never results or lifecycle state.
// Both transports use this function and the same admission/create path as generate.
func (s *Server) retryMedia(ctx context.Context, p identity.Principal, project string, input tool.ProjectTaskCreateInput, jobID string, args map[string]any) (*pebblestore.ProjectTaskRecord, error) {
	for _, key := range []string{"kind", "inputs", "title", "text", "voice", "style", "language", "format"} {
		if _, present := args[key]; present {
			return nil, fmt.Errorf("retry: argument %q cannot be applied", key)
		}
	}
	kind, sourceProject, id, err := controlMCPParseJobID(map[string]any{"job_id": jobID})
	if err != nil {
		return nil, errors.New("retry requires a valid media job_id")
	}
	if kind != controlMCPJobMedia || sourceProject != project {
		return nil, errors.New("retry requires a media job in the same project")
	}
	source, err := s.readableMediaTask(p, project, id)
	if err != nil {
		return nil, err
	}
	if !isGeneratableMediaAgent(source.Agent) {
		return nil, fmt.Errorf("retry: unsupported source kind %q", source.Agent)
	}
	if source.Operation == "speech" {
		return nil, errors.New("retry: does not support speech jobs; create a new speech request instead")
	}
	if controlMCPString(args, "operation") != "" && source.Agent != "video" {
		return nil, errors.New("retry: operation override is only supported for video jobs")
	}
	input.Agent = source.Agent
	input.Title, input.Prompt, input.Description = source.Description, source.Description, source.Description
	if input.Prompt == "" {
		input.Title, input.Prompt, input.Description = source.Title, source.Title, source.Title
	}
	input.Model, input.AspectRatio, input.Resolution = source.Model, source.AspectRatio, source.Resolution
	input.DurationSeconds, input.VariantCount, input.Operation = source.DurationSeconds, source.VariantCount, source.Operation
	input.AttachedMedia = append([]pebblestore.ProjectTaskMediaRef(nil), source.AttachedMedia...)
	if input.Agent == "video" {
		input.VideoType = "single"
	}
	for key, target := range map[string]*string{"model": &input.Model, "aspect_ratio": &input.AspectRatio, "resolution": &input.Resolution, "operation": &input.Operation} {
		if value := controlMCPString(args, key); value != "" {
			*target = value
		}
	}
	if strings.TrimSpace(controlMCPString(args, "brief")) != "" {
		input.Prompt = strings.TrimSpace(controlMCPString(args, "brief"))
		input.Title, input.Description = input.Prompt, input.Prompt
	}
	if input.Prompt == "" {
		return nil, errors.New("retry requires a nonempty brief")
	}
	if controlMCPInt(args, "count", 0) > 0 {
		input.VariantCount = controlMCPInt(args, "count", 0)
	}
	if controlMCPInt(args, "duration_seconds", 0) > 0 {
		input.DurationSeconds = controlMCPInt(args, "duration_seconds", 0)
	}
	input.MediaRetryOf = controlMCPJobID(controlMCPJobMedia, project, id)
	return s.createManagedMedia(ctx, p, project, input)
}

func (s *Server) createManagedMedia(ctx context.Context, p identity.Principal, project string, input tool.ProjectTaskCreateInput) (*pebblestore.ProjectTaskRecord, error) {
	if input.AutoApprove {
		if err := s.sessions.Store().RecordPermissionUse(p.AccountScopeID, "tasks", "", time.Now().UnixMilli()); err != nil {
			return nil, err
		}
	}
	input.ID = "task_" + uuid.NewString()
	estimate, err := s.estimateManagedMedia(ctx, p, &input)
	if err != nil {
		return nil, err
	}
	if input.OriginSessionID != "" {
		if err := s.sessions.Store().CheckWorkerMediaAdmission(p.AccountScopeID, input.OriginSessionID, estimate); err != nil {
			return nil, err
		}
	}
	// Pending approval is not an operation. The runner reserves atomically just
	// before dispatch, rechecking usage after any approval delay.
	return s.CreateProjectTask(ctx, p, project, input)
}

// Only generate's stored kinds may enter the priced media creation path.
func isGeneratableMediaAgent(agent string) bool {
	for _, kind := range controlMCPMediaKinds {
		if agent == kind {
			return true
		}
	}
	return false
}
