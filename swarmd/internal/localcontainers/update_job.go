package localcontainers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const PathContainerUpdateJob = "swarm.containers.local.update-job.v1"

type UpdateJobInput struct {
	DevMode          *bool
	TargetVersion    string
	PostRebuildCheck bool
}

type UpdateJobResult struct {
	PathID          string           `json:"path_id"`
	Mode            string           `json:"mode"`
	DevMode         bool             `json:"dev_mode"`
	Target          UpdatePlanTarget `json:"target"`
	Summary         UpdateJobSummary `json:"summary"`
	Items           []UpdateJobItem  `json:"items"`
	CheckedAtUnix   int64            `json:"checked_at_unix_ms,omitempty"`
	StartedAtUnix   int64            `json:"started_at_unix_ms,omitempty"`
	UpdatedAtUnix   int64            `json:"updated_at_unix_ms,omitempty"`
	CompletedAtUnix int64            `json:"completed_at_unix_ms,omitempty"`
}

type UpdateJobSummary struct {
	Total          int `json:"total"`
	Replaced       int `json:"replaced"`
	Skipped        int `json:"skipped"`
	Failed         int `json:"failed"`
	AlreadyCurrent int `json:"already_current"`
	Unknown        int `json:"unknown"`
}

type UpdateJobItem struct {
	ID                  string         `json:"id"`
	Name                string         `json:"name,omitempty"`
	ContainerName       string         `json:"container_name,omitempty"`
	Runtime             string         `json:"runtime,omitempty"`
	PreviousContainerID string         `json:"previous_container_id,omitempty"`
	ContainerID         string         `json:"container_id,omitempty"`
	PreviousImageRef    string         `json:"previous_image_ref,omitempty"`
	TargetImageRef      string         `json:"target_image_ref,omitempty"`
	TargetFingerprint   string         `json:"target_fingerprint,omitempty"`
	Status              string         `json:"status,omitempty"`
	State               string         `json:"state"`
	Reason              string         `json:"reason,omitempty"`
	Warning             string         `json:"warning,omitempty"`
	Error               string         `json:"error,omitempty"`
	Plan                UpdateItem     `json:"plan,omitempty"`
	Result              *ReplaceResult `json:"result,omitempty"`
}

func (s *Service) RunUpdateJob(ctx context.Context, input UpdateJobInput) (UpdateJobResult, error) {
	startedAt := time.Now().UnixMilli()
	result := UpdateJobResult{PathID: PathContainerUpdateJob, Items: []UpdateJobItem{}, StartedAtUnix: startedAt, UpdatedAtUnix: startedAt}
	if s == nil || s.store == nil {
		return result, errors.New("local container service is not configured")
	}
	plan, err := s.UpdatePlan(ctx, UpdatePlanInput{DevMode: input.DevMode, TargetVersion: input.TargetVersion, PostRebuildCheck: input.PostRebuildCheck})
	result.Mode = plan.Mode
	result.DevMode = plan.DevMode
	result.Target = plan.Target
	result.CheckedAtUnix = plan.CheckedAtUnix
	if err != nil {
		return result, err
	}
	for _, planned := range plan.Containers {
		item := UpdateJobItem{ID: planned.ID, Name: planned.Name, ContainerName: planned.ContainerName, Runtime: planned.Runtime, ContainerID: planned.ContainerID, PreviousImageRef: planned.StoredImageRef, TargetImageRef: planned.TargetImageRef, TargetFingerprint: planned.TargetFingerprint, Status: planned.Status, Plan: planned}
		switch planned.State {
		case "needs-update":
			replaceResult, replaceErr := s.Replace(ctx, ReplaceInput{ID: planned.ID, Target: plan.Target, DevMode: &plan.DevMode})
			item.Result = &replaceResult
			item.PreviousContainerID = replaceResult.PreviousContainerID
			item.ContainerID = firstNonEmpty(replaceResult.ContainerID, item.ContainerID)
			item.PreviousImageRef = firstNonEmpty(replaceResult.PreviousImageRef, item.PreviousImageRef)
			item.TargetImageRef = firstNonEmpty(replaceResult.TargetImageRef, item.TargetImageRef)
			item.TargetFingerprint = firstNonEmpty(replaceResult.TargetFingerprint, item.TargetFingerprint)
			item.Status = firstNonEmpty(replaceResult.Status, item.Status)
			item.Warning = replaceResult.Warning
			if replaceErr != nil {
				item.State = "failed"
				item.Error = replaceErr.Error()
				result.Summary.Failed++
			} else if replaceResult.State == "skipped" {
				item.State = "skipped"
				item.Reason = firstNonEmpty(replaceResult.Reason, "already-current")
				result.Summary.Skipped++
			} else {
				item.State = firstNonEmpty(replaceResult.State, "replaced")
				result.Summary.Replaced++
			}
		case "already-current":
			item.State = "skipped"
			item.Reason = "already-current"
			result.Summary.AlreadyCurrent++
			result.Summary.Skipped++
		default:
			item.State = "skipped"
			item.Reason = firstNonEmpty(planned.Reason, planned.State, "unknown")
			if strings.TrimSpace(planned.Error) != "" {
				item.Error = planned.Error
			}
			result.Summary.Unknown++
			result.Summary.Skipped++
		}
		result.Items = append(result.Items, item)
		result.Summary.Total++
		result.UpdatedAtUnix = time.Now().UnixMilli()
	}
	result.CompletedAtUnix = time.Now().UnixMilli()
	result.UpdatedAtUnix = result.CompletedAtUnix
	if result.Summary.Failed > 0 {
		return result, fmt.Errorf("local container update failed for %d container(s)", result.Summary.Failed)
	}
	return result, nil
}
