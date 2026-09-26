package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodel"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// computeTaskProgramDefinitionHash computes the SHA256 hex digest of a TaskProgramDefinition.
func computeTaskProgramDefinitionHash(def pebblestore.TaskProgramDefinition) string {
	raw, err := json.Marshal(def)
	if err != nil {
		return fmt.Sprintf("hash_%d", time.Now().UnixNano())
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// sanitizeBranchSlug turns arbitrary strings into a safe git branch segment.
func sanitizeBranchSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var out []rune
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			out = append(out, r)
		} else if r == ' ' || r == '_' || r == '/' || r == '.' {
			out = append(out, '-')
		}
	}
	res := strings.Trim(string(out), "-")
	if len(res) > 30 {
		res = res[:30]
	}
	if res == "" {
		res = "task"
	}
	return res
}

// hydrateTaskProgramStatus populates TaskProgramStatus from Pebble if a program ID exists.
func hydrateTaskProgramStatus(task *pebblestore.ProjectTaskRecord, db *pebblestore.SessionStore) {
	if task == nil || db == nil {
		return
	}
	if task.TaskProgramID != "" && task.SessionID != "" {
		if prog, ok, _ := db.GetTaskProgram(task.SessionID, task.TaskProgramID); ok {
			task.TaskProgramStatus = &prog
		}
	}
}

func findJobDef(jobs []pebblestore.TaskProgramJobSpec, jobID string) *pebblestore.TaskProgramJobSpec {
	for i := range jobs {
		if jobs[i].ID == jobID {
			return &jobs[i]
		}
	}
	return nil
}

func findJobRecord(jobs []pebblestore.TaskProgramJobRecord, jobID string) *pebblestore.TaskProgramJobRecord {
	for i := range jobs {
		if jobs[i].JobID == jobID {
			return &jobs[i]
		}
	}
	return nil
}

// readyJobIndexesForStage identifies declared jobs in the active stage whose dependencies are satisfied.
func readyJobIndexesForStage(record *pebblestore.TaskProgramRecord, stageID string) []int {
	if record == nil {
		return nil
	}
	var ready []int
	for i, job := range record.Jobs {
		if job.StageID != stageID || job.State != pebblestore.TaskProgramJobDeclared {
			continue
		}
		defJob := findJobDef(record.Definition.Jobs, job.JobID)
		if defJob == nil {
			continue
		}
		allDepsMet := true
		for _, depID := range defJob.DependsOn {
			depJob := findJobRecord(record.Jobs, depID)
			if depJob == nil || (depJob.State != pebblestore.TaskProgramJobIntegrated && depJob.State != pebblestore.TaskProgramJobCompleted) {
				allDepsMet = false
				break
			}
		}
		if allDepsMet {
			ready = append(ready, i)
		}
	}
	return ready
}

// deployProjectTaskProgram initializes and deploys a TaskProgram on a project task standalone.
func (s *Server) deployProjectTaskProgram(p identity.Principal, proj *pebblestore.ProjectRecord, task *pebblestore.ProjectTaskRecord) error {
	if task == nil {
		return errors.New("task is required")
	}
	db := s.sessions.Store()
	if db == nil {
		return errors.New("session store not available")
	}
	if task.TaskProgram == nil && task.TaskProgramID != "" && task.SessionID != "" {
		if existing, ok, _ := db.GetTaskProgram(task.SessionID, task.TaskProgramID); ok {
			task.TaskProgram = &existing.Definition
		}
	}
	if task.TaskProgram == nil {
		return errors.New("task program is required")
	}
	if len(task.TaskProgram.Stages) == 0 {
		return errors.New("task program requires at least one stage")
	}
	if len(task.TaskProgram.Jobs) == 0 {
		return errors.New("task program requires at least one job")
	}

	// 1. Ensure coordinator session exists
	now := time.Now().UnixMilli()
	if task.SessionID == "" {
		sessionID := sessionruntime.NewSessionID()
		task.SessionID = sessionID
		wsPath := strings.TrimSpace(task.WorkspacePath)
		if wsPath == "" && len(task.WorkspacesInvolved) > 0 {
			wsPath = task.WorkspacesInvolved[0]
			task.WorkspacePath = wsPath
		}
		if wsPath == "" && proj != nil && len(proj.Workspaces) > 0 {
			wsPath = proj.Workspaces[0].Path
			task.WorkspacePath = wsPath
		}
		if wsPath == "" {
			wsPath = "."
			task.WorkspacePath = wsPath
		}

		// Resolve default preference for coordinator
		pref := pebblestore.ModelPreference{Provider: "google", Model: "gemini-3.8-flash", Thinking: "low"}
		if s.model != nil {
			if def, err := s.model.ResolvePreference(pebblestore.ModelPreference{}); err == nil {
				pref = def.Preference
			}
		}

		sessionSnapshot := pebblestore.SessionSnapshot{
			ID:             sessionID,
			UserID:         p.UserID,
			AccountScopeID: p.AccountScopeID,
			WorkspacePath:  wsPath,
			WorkspaceName:  filepath.Base(wsPath),
			Title:          fmt.Sprintf("[%s] Coordinator: %s", proj.Name, task.Title),
			Mode:           sessionruntime.ModeAuto,
			Preference:     pref,
			Metadata: map[string]any{
				"project_id":      task.ProjectID,
				"task_id":         task.ID,
				"task_title":      task.Title,
				"role":            "task_program_coordinator",
				"task_program_id": task.TaskProgram.ID,
			},
			CreatedAt: now,
			UpdatedAt: now,
		}

		createKey := fmt.Sprintf("project-task:coordinator:%s:%d", sessionID, now)
		_, _ = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
			SessionID:       sessionID,
			UserID:          p.UserID,
			AccountScopeID:  p.AccountScopeID,
			ClientRequestID: createKey,
			IdempotencyKey:  createKey,
			PayloadHash:     createKey,
			RequestHash:     createKey,
			Kind:            sessionruntime.SessionMutationCreateSession,
			Session:         &sessionSnapshot,
		})
	}

	// 2. Initialize or get TaskProgramRecord in Pebble
	progID := strings.TrimSpace(task.TaskProgram.ID)
	if progID == "" {
		progID = fmt.Sprintf("prog_%s", task.ID)
		task.TaskProgram.ID = progID
	}

	initialRecord := pebblestore.TaskProgramRecord{
		ParentSessionID: task.SessionID,
		ProgramID:       progID,
		DefinitionHash:  computeTaskProgramDefinitionHash(*task.TaskProgram),
		Definition:      *task.TaskProgram,
		State:           pebblestore.TaskProgramStateRunning,
		ActiveStageID:   task.TaskProgram.Stages[0].ID,
		Jobs:            make([]pebblestore.TaskProgramJobRecord, len(task.TaskProgram.Jobs)),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	for i, j := range task.TaskProgram.Jobs {
		initialRecord.Jobs[i] = pebblestore.TaskProgramJobRecord{
			JobID:         j.ID,
			StageID:       j.StageID,
			State:         pebblestore.TaskProgramJobDeclared,
			AttemptNumber: 1,
			UpdatedAt:     now,
		}
	}

	record, _, err := db.CreateTaskProgram(initialRecord)
	if err != nil {
		// If already exists, load current
		if existing, ok, getErr := db.GetTaskProgram(task.SessionID, progID); getErr == nil && ok {
			record = existing
		} else {
			return fmt.Errorf("create task program record: %w", err)
		}
	}

	task.TaskProgramID = progID
	task.TaskProgramStatus = &record
	task.Status = "in_progress"
	task.ActionNeeded = "Task program executing parallel cohort..."
	if len(task.WhatDidDo) == 0 {
		task.WhatDidDo = []string{
			fmt.Sprintf("Deployed Task Program with %d job(s) across %d stage(s)", len(task.TaskProgram.Jobs), len(task.TaskProgram.Stages)),
		}
	}
	_ = db.PutProjectTask(p.AccountScopeID, task)

	// 3. Start autonomous background scheduler if runner/executor is active
	if s.v3SessionExecutor != nil || s.runner != nil {
		go s.executeStandaloneTaskProgram(p, task.ProjectID, task.ID)
	}
	return nil
}

// executeStandaloneTaskProgram runs the staged parallel execution loop for a task program.
func (s *Server) executeStandaloneTaskProgram(p identity.Principal, projectID, taskID string) {
	db := s.sessions.Store()
	if db == nil {
		return
	}

	task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
	if err != nil || !found || task == nil || task.TaskProgramID == "" || task.SessionID == "" {
		return
	}

	for {
		record, ok, err := db.GetTaskProgram(task.SessionID, task.TaskProgramID)
		if err != nil || !ok {
			return
		}

		if record.State == pebblestore.TaskProgramStateCompleted ||
			record.State == pebblestore.TaskProgramStateFailed ||
			record.State == pebblestore.TaskProgramStateBlocked ||
			record.State == pebblestore.TaskProgramStateCancelled {
			return
		}

		activeStageID := record.ActiveStageID
		if activeStageID == "" && len(record.Definition.Stages) > 0 {
			activeStageID = record.Definition.Stages[0].ID
		}

		// Find stage index
		stageIdx := -1
		for idx, st := range record.Definition.Stages {
			if st.ID == activeStageID {
				stageIdx = idx
				break
			}
		}
		if stageIdx < 0 {
			return
		}

		readyIndexes := readyJobIndexesForStage(&record, activeStageID)

		// 1. Launch ready jobs in parallel
		if len(readyIndexes) > 0 {
			var jobTransitions []pebblestore.TaskProgramJobTransition

			for _, idx := range readyIndexes {
				jobDef := record.Definition.Jobs[idx]
				jobRec := &record.Jobs[idx]

				childSessionID := sessionruntime.NewSessionID()
				branchName := fmt.Sprintf("agent/task-%s-%s-att%d", sanitizeBranchSlug(task.ID), sanitizeBranchSlug(jobDef.ID), jobRec.AttemptNumber)

				// Workspace allocation
				baseWs := task.WorkspacePath
				if baseWs == "" {
					baseWs = "."
				}
				childWsPath := baseWs
				baseCommit := ""

				if s.worktrees != nil && (jobDef.AgentType == "coder" || jobDef.AgentType == "" || jobDef.AgentType == "swarm") {
					if alloc, err := s.worktrees.AllocateDetachedWorkspaceRequestedForPrincipal(p, baseWs, childSessionID, "", branchName); err == nil && alloc.WorkspacePath != "" {
						childWsPath = alloc.WorkspacePath
						branchName = alloc.BranchName
						baseCommit = alloc.BaseCommit
					}
				}

				// Model resolution
				pref := pebblestore.ModelPreference{Provider: "google", Model: "gemini-3.8-flash", Thinking: "low"}
				subagentName := jobDef.AgentType
				if subagentName == "" {
					subagentName = "coder"
				}
				if canonicalID, isCanonical := agentruntime.CanonicalSystemAgentID(subagentName); isCanonical {
					if resolved, _, err := agentmodel.ResolveSystemAgent(s.model, s.agents, s.agentModelSettings, p.AccountScopeID, canonicalID, ""); err == nil && resolved.Preference.Model != "" {
						pref = resolved.Preference
					}
				}

				now := time.Now().UnixMilli()
				sessionSnapshot := pebblestore.SessionSnapshot{
					ID:               childSessionID,
					UserID:           p.UserID,
					AccountScopeID:   p.AccountScopeID,
					WorkspacePath:    childWsPath,
					WorkspaceName:    filepath.Base(childWsPath),
					Title:            fmt.Sprintf("[%s] %s", task.Title, jobDef.Title),
					Mode:             sessionruntime.ModeAuto,
					Preference:       pref,
					WorktreeEnabled:  childWsPath != baseWs,
					WorktreeRootPath: childWsPath,
					WorktreeBranch:   branchName,
					Metadata: map[string]any{
						"project_id":          task.ProjectID,
						"project_task_id":     task.ID,
						"task_program_id":     task.TaskProgramID,
						"task_program_job_id": jobDef.ID,
						"parent_session_id":   task.SessionID,
						"owned_scope":         jobDef.OwnedScope,
						"base_commit":         baseCommit,
						"role":                "task_program_child",
					},
					CreatedAt: now,
					UpdatedAt: now,
				}

				createKey := fmt.Sprintf("child:create:%s:%d", childSessionID, now)
				_, _ = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
					SessionID:       childSessionID,
					UserID:          p.UserID,
					AccountScopeID:  p.AccountScopeID,
					ClientRequestID: createKey,
					IdempotencyKey:  createKey,
					PayloadHash:     createKey,
					RequestHash:     createKey,
					Kind:            sessionruntime.SessionMutationCreateSession,
					Session:         &sessionSnapshot,
				})

				// Seed prompt
				promptBuilder := strings.Builder{}
				promptBuilder.WriteString(fmt.Sprintf("## Task Program Job: %s\n\n", jobDef.Title))
				promptBuilder.WriteString(fmt.Sprintf("%s\n\n", jobDef.MetaPrompt))
				if jobDef.Deliverable != "" {
					promptBuilder.WriteString(fmt.Sprintf("**Deliverable:** %s\n", jobDef.Deliverable))
				}
				if len(jobDef.OwnedScope) > 0 {
					promptBuilder.WriteString(fmt.Sprintf("**Owned Scope:** %s\n", strings.Join(jobDef.OwnedScope, ", ")))
				}
				if len(jobDef.AcceptanceCriteria) > 0 {
					promptBuilder.WriteString("\n**Acceptance Criteria:**\n")
					for _, ac := range jobDef.AcceptanceCriteria {
						promptBuilder.WriteString(fmt.Sprintf("- [ ] %s\n", ac))
					}
				}

				runID := fmt.Sprintf("desktop-v3-run:%s", sessionruntime.NewSessionID())
				msgID := fmt.Sprintf("msg_%s_%d", childSessionID, now)
				msg := pebblestore.MessageSnapshot{
					ID:             msgID,
					SessionID:      childSessionID,
					UserID:         p.UserID,
					AccountScopeID: p.AccountScopeID,
					Role:           "user",
					Content:        promptBuilder.String(),
					CreatedAt:      now,
				}
				runIntent := &pebblestore.V3SessionRunIntent{
					SessionID:       childSessionID,
					RunID:           runID,
					EpochID:         "epoch-00000000000000000001",
					UserID:          p.UserID,
					AccountScopeID:  p.AccountScopeID,
					ParentSessionID: task.SessionID,
					SourceMessageID: msgID,
					Status:          pebblestore.V3RunIntentPendingExecutor,
					CreatedAt:       now,
					UpdatedAt:       now,
				}

				msgKey := fmt.Sprintf("child:seed:%s:%d", childSessionID, now)
				_, _ = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
					SessionID:       childSessionID,
					UserID:          p.UserID,
					AccountScopeID:  p.AccountScopeID,
					ClientRequestID: msgKey,
					IdempotencyKey:  msgKey,
					PayloadHash:     msgKey,
					RequestHash:     msgKey,
					Kind:            pebblestore.V3SessionMutationAppendMessage,
					Message:         &msg,
					RunIntent:       runIntent,
					NowUnixMs:       now,
				})

				s.EnqueueSessionRun(p, childSessionID, runID, task.SessionID)

				runningState := pebblestore.TaskProgramJobRunning
				jobTransitions = append(jobTransitions, pebblestore.TaskProgramJobTransition{
					JobID:              jobDef.ID,
					State:              runningState,
					ChildSessionID:     childSessionID,
					CurrentSessionID:   childSessionID,
					CurrentRunID:       runID,
					WorktreeBranch:     branchName,
					WorkspacePath:      childWsPath,
					ImmutableStageBase: baseCommit,
				})
			}

			// Transition jobs to running
			mutationID := fmt.Sprintf("launch_jobs:%s:%d", activeStageID, time.Now().UnixMilli())
			runningProgramState := pebblestore.TaskProgramStateRunning
			record, _, _ = db.TransitionTaskProgram(task.SessionID, task.TaskProgramID, pebblestore.TaskProgramTransition{
				ExpectedRevision: record.Revision,
				MutationID:       mutationID,
				State:            &runningProgramState,
				Jobs:             jobTransitions,
			})
			_, _ = db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
				t.TaskProgramStatus = &record
				t.ActionNeeded = fmt.Sprintf("Executing Stage %s (%d jobs running)", activeStageID, len(jobTransitions))
				return nil
			})
		}

		// 2. Poll running jobs until complete
		hasRunning := false
		for _, job := range record.Jobs {
			if job.StageID == activeStageID && job.State == pebblestore.TaskProgramJobRunning {
				hasRunning = true
				break
			}
		}

		if hasRunning {
			if s.v3SessionExecutor == nil && s.runner == nil {
				// In unit tests without an active background executor, return after first transition
				return
			}
			time.Sleep(1 * time.Second)

			var completedTransitions []pebblestore.TaskProgramJobTransition
			for _, job := range record.Jobs {
				if job.StageID != activeStageID || job.State != pebblestore.TaskProgramJobRunning {
					continue
				}
				childSess, found, _ := db.GetSession(job.ChildSessionID)
				if !found {
					continue
				}
				// If child is no longer active and has finished run
				if (childSess.Lifecycle == nil || !childSess.Lifecycle.Active) && childSess.MessageCount > 1 {
					// Inspect worktree head commit
					headCommit := ""
					if job.WorkspacePath != "" {
						ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
						cmd := exec.CommandContext(ctx, "git", "-C", job.WorkspacePath, "rev-parse", "HEAD")
						if out, err := cmd.Output(); err == nil && len(bytes.TrimSpace(out)) > 0 {
							headCommit = strings.TrimSpace(string(out))
						}
						cancel()
					}

					readyJobState := pebblestore.TaskProgramJobHandoffReady
					completedTransitions = append(completedTransitions, pebblestore.TaskProgramJobTransition{
						JobID:            job.JobID,
						State:            readyJobState,
						ChildHead:        headCommit,
						IntegrationState: "ready",
					})
				}
			}

			if len(completedTransitions) > 0 {
				mutationID := fmt.Sprintf("jobs_done:%s:%d", activeStageID, time.Now().UnixMilli())
				record, _, _ = db.TransitionTaskProgram(task.SessionID, task.TaskProgramID, pebblestore.TaskProgramTransition{
					ExpectedRevision: record.Revision,
					MutationID:       mutationID,
					Jobs:             completedTransitions,
				})
				_, _ = db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
					t.TaskProgramStatus = &record
					for _, ct := range completedTransitions {
						t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Subagent finished job %s", ct.JobID))
					}
					return nil
				})
			}
			continue
		}

		// 3. Check if all jobs in this stage are handoff_ready
		allStageJobsReady := true
		for _, job := range record.Jobs {
			if job.StageID == activeStageID {
				if job.State != pebblestore.TaskProgramJobHandoffReady &&
					job.State != pebblestore.TaskProgramJobIntegrated &&
					job.State != pebblestore.TaskProgramJobCompleted {
					allStageJobsReady = false
					break
				}
			}
		}

		if allStageJobsReady {
			// Stage Integration Barrier!
			var children []worktreeruntime.TaskIntegrationChild
			for _, job := range record.Jobs {
				if job.StageID == activeStageID && job.State == pebblestore.TaskProgramJobHandoffReady {
					def := findJobDef(record.Definition.Jobs, job.JobID)
					scopes := []string{}
					if def != nil {
						scopes = def.OwnedScope
					}
					baseCommit := job.ImmutableStageBase
					if baseCommit == "" {
						baseCommit = task.BaseCommit
					}
					if job.ChildHead != "" && baseCommit != "" && job.ChildHead != baseCommit {
						children = append(children, worktreeruntime.TaskIntegrationChild{
							SessionID:   job.ChildSessionID,
							BaseCommit:  baseCommit,
							HeadCommit:  job.ChildHead,
							OwnedScopes: scopes,
						})
					}
				}
			}

			parentWs := task.WorkspacePath
			if parentWs == "" {
				parentWs = "."
			}

			worktreeSvc := &worktreeruntime.Service{}
			parentState, pErr := worktreeSvc.InspectTaskWorkspace(parentWs)
			if pErr != nil {
				// Mark program blocked
				blocker := &pebblestore.TaskProgramBlocker{
					Code:    "workspace_inspect_error",
					Message: fmt.Sprintf("Failed inspecting workspace %s: %v", parentWs, pErr),
				}
				blockedState := pebblestore.TaskProgramStateBlocked
				record, _, _ = db.TransitionTaskProgram(task.SessionID, task.TaskProgramID, pebblestore.TaskProgramTransition{
					ExpectedRevision: record.Revision,
					MutationID:       fmt.Sprintf("blocker:%d", time.Now().UnixMilli()),
					State:            &blockedState,
					Blocker:          blocker,
				})
				_, _ = db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
					t.TaskProgramStatus = &record
					t.ActionNeeded = blocker.Message
					return nil
				})
				return
			}

			if len(children) > 0 {
				plan, planErr := worktreeSvc.PrepareTaskIntegration(parentWs, parentState.BranchName, parentState.HeadCommit, children)
				if planErr != nil {
					// CONFLICT!
					conflictedJobID := ""
					if conflictErr, ok := planErr.(*worktreeruntime.TaskIntegrationConflictError); ok {
						for _, j := range record.Jobs {
							if j.ChildSessionID == conflictErr.SessionID {
								conflictedJobID = j.JobID
								break
							}
						}
					}
					if conflictedJobID == "" && len(record.Jobs) > 0 {
						for _, j := range record.Jobs {
							if j.StageID == activeStageID {
								conflictedJobID = j.JobID
								break
							}
						}
					}

					var jobTransitions []pebblestore.TaskProgramJobTransition
					conflictJobState := "conflict"
					for _, j := range record.Jobs {
						if j.JobID == conflictedJobID {
							jobTransitions = append(jobTransitions, pebblestore.TaskProgramJobTransition{
								JobID:            j.JobID,
								State:            conflictJobState,
								IntegrationState: "conflict",
							})
						}
					}

					blockedState := pebblestore.TaskProgramStateBlocked
					blocker := &pebblestore.TaskProgramBlocker{
						Code:    "integration_conflict",
						Message: fmt.Sprintf("Merge conflict integrating job %s: %v", conflictedJobID, planErr),
					}
					record, _, _ = db.TransitionTaskProgram(task.SessionID, task.TaskProgramID, pebblestore.TaskProgramTransition{
						ExpectedRevision: record.Revision,
						MutationID:       fmt.Sprintf("conflict:%d", time.Now().UnixMilli()),
						State:            &blockedState,
						Blocker:          blocker,
						Jobs:             jobTransitions,
					})

					_, _ = db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
						t.TaskProgramStatus = &record
						t.Status = "needs_review"
						t.ActionNeeded = fmt.Sprintf("Merge conflict on job %s: redeployment required", conflictedJobID)
						t.LastError = blocker.Message
						return nil
					})
					return
				}

				// Apply integration cleanly!
				if len(plan.Commits) > 0 {
					_, applyErr := worktreeSvc.ApplyTaskIntegration(parentWs, plan)
					if applyErr != nil {
						blockedState := pebblestore.TaskProgramStateBlocked
						blocker := &pebblestore.TaskProgramBlocker{
							Code:    "integration_apply_error",
							Message: fmt.Sprintf("Failed applying git integration: %v", applyErr),
						}
						record, _, _ = db.TransitionTaskProgram(task.SessionID, task.TaskProgramID, pebblestore.TaskProgramTransition{
							ExpectedRevision: record.Revision,
							MutationID:       fmt.Sprintf("apply_error:%d", time.Now().UnixMilli()),
							State:            &blockedState,
							Blocker:          blocker,
						})
						_, _ = db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
							t.TaskProgramStatus = &record
							t.ActionNeeded = blocker.Message
							return nil
						})
						return
					}
				}
			}

			// Stage integration succeeded! Mark all stage jobs integrated!
			var integratedTransitions []pebblestore.TaskProgramJobTransition
			integratedJobState := pebblestore.TaskProgramJobIntegrated
			for _, j := range record.Jobs {
				if j.StageID == activeStageID {
					integratedTransitions = append(integratedTransitions, pebblestore.TaskProgramJobTransition{
						JobID:            j.JobID,
						State:            integratedJobState,
						IntegrationState: "integrated",
					})
				}
			}

			// Check if there is another stage
			if stageIdx+1 < len(record.Definition.Stages) {
				nextStageID := record.Definition.Stages[stageIdx+1].ID
				runningProgState := pebblestore.TaskProgramStateRunning
				record, _, _ = db.TransitionTaskProgram(task.SessionID, task.TaskProgramID, pebblestore.TaskProgramTransition{
					ExpectedRevision: record.Revision,
					MutationID:       fmt.Sprintf("advance_stage:%s:%d", nextStageID, time.Now().UnixMilli()),
					State:            &runningProgState,
					ActiveStageID:    &nextStageID,
					Jobs:             integratedTransitions,
				})
				_, _ = db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
					t.TaskProgramStatus = &record
					t.ActionNeeded = fmt.Sprintf("Stage %s integrated. Advancing to Stage %s...", activeStageID, nextStageID)
					t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Integrated all jobs in Stage %s", activeStageID))
					return nil
				})
				continue
			}

			// All stages completed!
			completedProgState := pebblestore.TaskProgramStateCompleted
			record, _, _ = db.TransitionTaskProgram(task.SessionID, task.TaskProgramID, pebblestore.TaskProgramTransition{
				ExpectedRevision: record.Revision,
				MutationID:       fmt.Sprintf("completed:%d", time.Now().UnixMilli()),
				State:            &completedProgState,
				Jobs:             integratedTransitions,
			})

			gitState := inspectTaskGitState(*task, db)
			_, _ = db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
				t.TaskProgramStatus = &record
				t.Status = "needs_review"
				t.UnintegratedCommits = gitState.unintegratedCommits
				t.DiffSummary = gitState.diffSummary
				t.IsIntegrated = false
				t.ActionNeeded = "Action Needed: All task program jobs finished and integrated. Ready to integrate into dev/main."
				t.WhatDidDo = append(t.WhatDidDo, "All Task Program stages completed and integrated")
				return nil
			})
			return
		}
	}
}

// redeployTaskProgramJob redeploys a conflicted or failed job within the task program.
func (s *Server) redeployTaskProgramJob(p identity.Principal, projectID, taskID, jobID, feedback string) error {
	db := s.sessions.Store()
	if db == nil {
		return errors.New("session store not available")
	}

	task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
	if err != nil || !found || task == nil {
		return errors.New("project task not found")
	}
	if task.TaskProgramID == "" || task.SessionID == "" {
		return errors.New("task has no associated task program")
	}

	record, ok, err := db.GetTaskProgram(task.SessionID, task.TaskProgramID)
	if err != nil || !ok {
		return errors.New("task program not found")
	}

	targetJob := findJobRecord(record.Jobs, jobID)
	if targetJob == nil {
		return fmt.Errorf("job %q not found in task program", jobID)
	}

	now := time.Now().UnixMilli()
	newAttempt := targetJob.AttemptNumber + 1

	// Add generation record
	genHistory := append([]pebblestore.TaskProgramJobGeneration(nil), targetJob.GenerationHistory...)
	genHistory = append(genHistory, pebblestore.TaskProgramJobGeneration{
		Generation: targetJob.AttemptNumber,
		SessionID:  targetJob.ChildSessionID,
		RunID:      targetJob.CurrentRunID,
		State:      targetJob.State,
		StartedAt:  now,
		FinishedAt: now,
	})

	declaredState := pebblestore.TaskProgramJobDeclared
	runningState := pebblestore.TaskProgramStateRunning

	jobTransition := pebblestore.TaskProgramJobTransition{
		JobID:             jobID,
		State:             declaredState,
		AttemptNumber:     newAttempt,
		GenerationHistory: genHistory,
		IntegrationState:  "",
	}

	// Update record
	updatedRecord, _, err := db.TransitionTaskProgram(task.SessionID, task.TaskProgramID, pebblestore.TaskProgramTransition{
		ExpectedRevision: record.Revision,
		MutationID:       fmt.Sprintf("redeploy:%s:%d", jobID, now),
		State:            &runningState,
		ClearBlocker:     true,
		Jobs:             []pebblestore.TaskProgramJobTransition{jobTransition},
	})
	if err != nil {
		return fmt.Errorf("transition task program for redeploy: %w", err)
	}

	// Also update attempt number directly on job
	for i := range updatedRecord.Jobs {
		if updatedRecord.Jobs[i].JobID == jobID {
			break
		}
	}

	fb := strings.TrimSpace(feedback)
	_, _ = db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
		t.TaskProgramStatus = &updatedRecord
		t.Status = "in_progress"
		t.ActionNeeded = fmt.Sprintf("Redeploying job %s (Attempt %d)...", jobID, newAttempt)
		if fb != "" {
			t.FeedbackHistory = append(t.FeedbackHistory, fmt.Sprintf("Job %s retry feedback: %s", jobID, fb))
			t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Redeployed job %s with conflict resolution instructions", jobID))
		} else {
			t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Redeployed job %s (Attempt %d)", jobID, newAttempt))
		}
		return nil
	})

	// Restart standalone execution if runner/executor is active
	if s.v3SessionExecutor != nil || s.runner != nil {
		go s.executeStandaloneTaskProgram(p, projectID, taskID)
	}
	return nil
}

// DeployProjectTask deploys a project task execution or standalone Task Program for the given project and task ID.
func (s *Server) DeployProjectTask(accountScopeID, projectID, taskID string) error {
	p := identity.Principal{AccountScopeID: accountScopeID}
	db := s.sessions.Store()
	if db == nil {
		return errors.New("database not available")
	}
	proj, found, err := db.GetProject(accountScopeID, projectID)
	if err != nil {
		return err
	}
	if !found || proj == nil {
		return fmt.Errorf("project %q not found", projectID)
	}
	task, found, err := db.GetProjectTask(accountScopeID, projectID, taskID)
	if err != nil {
		return err
	}
	if !found || task == nil {
		return fmt.Errorf("task %q not found", taskID)
	}
	task.Status = "in_progress"
	task.ActionNeeded = ""
	if task.TaskProgram != nil || task.TaskProgramID != "" {
		if err := s.deployProjectTaskProgram(p, proj, task); err != nil {
			return err
		}
		return nil
	} else if task.Agent == "image" || task.Agent == "video" {
		if err := s.deployProjectTaskExecution(p, proj, task, "in_progress", ""); err != nil {
			return err
		}
	} else {
		if task.SessionID == "" {
			if err := s.deployProjectTaskExecution(p, proj, task, "in_progress", task.Title); err != nil {
				return err
			}
		}
	}
	return db.PutProjectTask(accountScopeID, task)
}
