package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodel"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	taskrouter "swarm/packages/swarmd/internal/taskrouter"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

const ProjectsPath = "/v3/projects"

type taskGitState struct {
	worktreeBranch      string
	worktreeName        string
	baseBranch          string
	baseCommit          string
	unintegratedCommits int
	behindCommits       int
	isIntegrated        bool
	diffSummary         string
	isDirty             bool
	dirtyCount          int
	actionNeeded        string
	syncWarning         string
}

// inspectTaskGitState probes a workspace/worktree path for dirty status and unintegrated commits.
func inspectTaskGitState(task pebblestore.ProjectTaskRecord, db *pebblestore.SessionStore) taskGitState {
	res := taskGitState{
		worktreeBranch: task.WorktreeBranch,
		worktreeName:   task.WorktreeName,
		baseBranch:     task.BaseBranch,
	}
	if res.worktreeBranch == "" || res.worktreeBranch == "main" || res.worktreeBranch == "dev" || res.worktreeBranch == "master" {
		res.worktreeBranch, res.worktreeName = pebblestore.MakeWorktreeBranch(task.Title, task.Description)
	}
	if res.worktreeName == "" {
		res.worktreeName = strings.TrimPrefix(res.worktreeBranch, "agent/")
		res.worktreeName = strings.TrimPrefix(res.worktreeName, "worktree/")
	}
	if res.baseBranch == "" {
		res.baseBranch = "main"
	}

	// 1. Don't show or inspect git state for pending approval, planning, or queued tasks
	if task.Status == "pending_approval" || task.Status == "planning" || task.Status == "queued" {
		return res
	}
	// 2. Media tasks don't have git tracking
	if task.Agent == "image" || task.Agent == "video" {
		return res
	}

	// 3. Resolve session and target path
	var sess *pebblestore.SessionSnapshot
	if task.SessionID != "" && db != nil {
		if s, found, _ := db.GetSession(task.SessionID); found {
			sess = &s
		}
	}

	targetPath := strings.TrimSpace(task.WorkspacePath)
	if sess != nil && sess.WorktreeEnabled && strings.TrimSpace(sess.WorktreeRootPath) != "" {
		targetPath = strings.TrimSpace(sess.WorktreeRootPath)
	}
	if targetPath == "" {
		return res
	}
	if fi, err := os.Stat(targetPath); err != nil || !fi.IsDir() {
		return res
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 4. Resolve base branch
	if sess != nil && strings.TrimSpace(sess.WorktreeBaseBranch) != "" {
		res.baseBranch = strings.TrimSpace(sess.WorktreeBaseBranch)
	} else {
		// Test if dev exists
		if err := exec.CommandContext(ctx, "git", "-C", targetPath, "rev-parse", "--verify", "dev").Run(); err == nil {
			res.baseBranch = "dev"
		} else if err := exec.CommandContext(ctx, "git", "-C", targetPath, "rev-parse", "--verify", "origin/dev").Run(); err == nil {
			res.baseBranch = "dev"
		}
	}

	// 5. Inspect current HEAD branch and commit in targetPath
	cmdHead := exec.CommandContext(ctx, "git", "-C", targetPath, "rev-parse", "--abbrev-ref", "HEAD")
	headBranch := ""
	if out, err := cmdHead.Output(); err == nil {
		headBranch = strings.TrimSpace(string(out))
	}
	cmdHeadCommit := exec.CommandContext(ctx, "git", "-C", targetPath, "rev-parse", "HEAD")
	headCommit := ""
	if out, err := cmdHeadCommit.Output(); err == nil {
		headCommit = strings.TrimSpace(string(out))
	}

	isTaskWorktree := (sess != nil && sess.WorktreeEnabled && sess.WorktreeRootPath == targetPath) ||
		(headBranch != "" && headBranch != "main" && headBranch != "dev" && headBranch != "master" && (headBranch == res.worktreeBranch || strings.HasPrefix(headBranch, "agent/")))

	if !isTaskWorktree {
		// targetPath is the main repo or non-worktree checkout — do NOT attribute its dirty status or diff to this task!
		return res
	}

	// 6. Dirty status
	cmdDirty := exec.CommandContext(ctx, "git", "-C", targetPath, "status", "--porcelain")
	if out, err := cmdDirty.Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		for _, l := range lines {
			if strings.TrimSpace(l) != "" {
				res.dirtyCount++
			}
		}
		res.isDirty = res.dirtyCount > 0
	}

	// 7. Resolve base commit of worktree (fork commit)
	baseCommit := strings.TrimSpace(res.baseCommit)
	if baseCommit == "" && sess != nil && sess.Metadata != nil {
		if bc, ok := sess.Metadata["base_commit"].(string); ok {
			baseCommit = strings.TrimSpace(bc)
		}
	}
	if baseCommit == "" {
		// Check reflog to find where the branch was created
		cmdReflog := exec.CommandContext(ctx, "git", "-C", targetPath, "log", "-g", "--format=%H", "--reverse", fmt.Sprintf("refs/heads/%s", res.worktreeBranch))
		if out, err := cmdReflog.Output(); err == nil {
			fields := strings.Fields(strings.TrimSpace(string(out)))
			if len(fields) > 0 {
				baseCommit = fields[0]
			}
		}
	}
	if baseCommit == "" {
		// Fallback to merge-base between baseBranch and HEAD
		cmdMb := exec.CommandContext(ctx, "git", "-C", targetPath, "merge-base", res.baseBranch, "HEAD")
		if out, err := cmdMb.Output(); err == nil && len(bytes.TrimSpace(out)) > 0 {
			baseCommit = strings.TrimSpace(string(out))
		}
	}
	res.baseCommit = baseCommit

	// 8. Ahead / behind relative to baseBranch
	cmdRevList := exec.CommandContext(ctx, "git", "-C", targetPath, "rev-list", "--left-right", "--count", res.baseBranch+"...HEAD")
	if out, err := cmdRevList.Output(); err == nil {
		fields := strings.Fields(strings.TrimSpace(string(out)))
		if len(fields) >= 2 {
			fmt.Sscanf(fields[0], "%d", &res.behindCommits)
			fmt.Sscanf(fields[1], "%d", &res.unintegratedCommits)
		}
	} else {
		cmdRevOrigin := exec.CommandContext(ctx, "git", "-C", targetPath, "rev-list", "--left-right", "--count", "origin/"+res.baseBranch+"...HEAD")
		if out, err := cmdRevOrigin.Output(); err == nil {
			fields := strings.Fields(strings.TrimSpace(string(out)))
			if len(fields) >= 2 {
				fmt.Sscanf(fields[0], "%d", &res.behindCommits)
				fmt.Sscanf(fields[1], "%d", &res.unintegratedCommits)
			}
		}
	}

	// 9. Diff summary
	if res.unintegratedCommits > 0 || res.isDirty {
		cmdDiff := exec.CommandContext(ctx, "git", "-C", targetPath, "diff", "--shortstat", res.baseBranch+"...HEAD")
		if out, err := cmdDiff.Output(); err == nil && len(bytes.TrimSpace(out)) > 0 {
			res.diffSummary = string(bytes.TrimSpace(out))
		}
	}

	// 10. True Git Integration Check:
	// A worktree branch is ONLY integrated if:
	// a) It is clean (not dirty), AND
	// b) It actually made commits beyond its fork base commit (HEAD != baseCommit), AND
	// c) All those commits are ancestors of baseBranch (merge-base --is-ancestor HEAD baseBranch == 0),
	//    meaning unintegratedCommits == 0.
	// If the branch NEVER made any commits (HEAD == baseCommit), it is NOT integrated.
	hasBranchCommits := false
	if headCommit != "" && baseCommit != "" && headCommit != baseCommit {
		hasBranchCommits = true
	} else if headCommit != "" {
		// Double check via reflog if commits were made on the branch
		cmdRefLogCommits := exec.CommandContext(ctx, "git", "-C", targetPath, "log", "-g", "--format=%gs", fmt.Sprintf("refs/heads/%s", res.worktreeBranch))
		if out, err := cmdRefLogCommits.Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "commit") {
					hasBranchCommits = true
					break
				}
			}
		}
	}

	if !res.isDirty && hasBranchCommits && res.unintegratedCommits == 0 && headCommit != "" {
		// Verify ancestry: is HEAD an ancestor of baseBranch?
		cmdAncestor := exec.CommandContext(ctx, "git", "-C", targetPath, "merge-base", "--is-ancestor", headCommit, res.baseBranch)
		if cmdAncestor.Run() == nil {
			res.isIntegrated = true
		} else {
			cmdAncestorOrigin := exec.CommandContext(ctx, "git", "-C", targetPath, "merge-base", "--is-ancestor", headCommit, "origin/"+res.baseBranch)
			if cmdAncestorOrigin.Run() == nil {
				res.isIntegrated = true
			}
		}
	}

	// 11. Sync Warning check (behind or out of sync)
	if res.behindCommits > 0 {
		res.syncWarning = fmt.Sprintf("Worktree is behind %s by %d commit(s) — rebase may be needed.", res.baseBranch, res.behindCommits)
	}

	// Also check if base branch in root workspace is behind upstream remote
	parentWs := task.WorkspacePath
	if sess != nil && sess.Metadata != nil {
		if src, ok := sess.Metadata["swarm_v3_source_workspace_path"].(string); ok && src != "" {
			parentWs = src
		}
	}
	if parentWs != "" && parentWs != targetPath {
		cmdBranchStatus := exec.CommandContext(ctx, "git", "-C", parentWs, "status", "--porcelain=v2", "--branch")
		if out, err := cmdBranchStatus.Output(); err == nil {
			outStr := string(out)
			for _, line := range strings.Split(outStr, "\n") {
				if strings.HasPrefix(line, "# branch.ab ") {
					var ahead, behind int
					if _, scanErr := fmt.Sscanf(line, "# branch.ab +%d -%d", &ahead, &behind); scanErr == nil {
						if behind > 0 {
							if res.syncWarning != "" {
								res.syncWarning = fmt.Sprintf("%s branch could be out of sync (behind upstream by %d commit(s)), and worktree is behind %s by %d commit(s).", res.baseBranch, behind, res.baseBranch, res.behindCommits)
							} else {
								res.syncWarning = fmt.Sprintf("%s branch could be out of sync (behind upstream remote by %d commit(s)).", res.baseBranch, behind)
							}
						}
					}
					break
				}
			}
		}
	}

	// 12. Action Needed
	if res.unintegratedCommits > 0 {
		res.actionNeeded = fmt.Sprintf("Action Needed: %d unintegrated commit(s) on %s ready to integrate.", res.unintegratedCommits, res.worktreeBranch)
	} else if res.isDirty {
		res.actionNeeded = fmt.Sprintf("Action Needed: %d modified file(s) waiting to be committed.", res.dirtyCount)
	} else if res.isIntegrated {
		res.actionNeeded = fmt.Sprintf("No Action Required: Integrated into %s", res.baseBranch)
	} else {
		res.actionNeeded = ""
	}

	return res
}

// syncTaskSessionState checks the live V3 session and plan for a task and transitions
// in_progress tasks to needs_review when the agent finishes execution, ensuring tasks
// never just flip to complete without review/integration, and allowing reopening.
func syncTaskSessionState(task *pebblestore.ProjectTaskRecord, db *pebblestore.SessionStore) {
	if task == nil || task.SessionID == "" || db == nil {
		return
	}
	// Do not override tasks awaiting user approval, planning, or queued
	if task.Status == "pending_approval" || task.Status == "planning" || task.Status == "queued" {
		return
	}

	sess, found, err := db.GetSession(task.SessionID)
	if err != nil || !found {
		return
	}

	// 1. If task is already integrated, it is completed
	if task.IsIntegrated {
		task.Status = "completed"
		return
	}

	// 2. If task was explicitly marked completed and not reopened, preserve completed
	if task.Status == "completed" {
		return
	}

	// 3. Inspect session lifecycle
	if sess.Lifecycle != nil {
		if sess.Lifecycle.Active {
			task.Status = "in_progress"
		} else if sess.MessageCount > 1 {
			// Session run has concluded:
			// In Swarm V3 orchestration, when an agent finishes execution, the task
			// transitions to needs_review for user review and git integration,
			// NEVER directly to completed!
			if task.Status == "in_progress" {
				task.Status = "needs_review"
				if task.ActionNeeded == "" || strings.HasPrefix(task.ActionNeeded, "Action Needed: 0") || task.ActionNeeded == "Executing reopened task" {
					if task.UnintegratedCommits > 0 {
						task.ActionNeeded = fmt.Sprintf("Action Needed: Review changes and integrate %d commit(s) into %s", task.UnintegratedCommits, task.BaseBranch)
					} else {
						task.ActionNeeded = "Action Needed: Review agent deliverables and verify outcomes"
					}
				}
			}
		}
	}

	// 4. Inspect session plans for waiting_review or needs_review status
	plans, _ := db.ListPlans(task.SessionID, 1)
	if len(plans) > 0 {
		plan := plans[0]
		if plan.Document != nil {
			if plan.Document.ExecutionState != nil && plan.Document.ExecutionState.Status == "waiting_review" {
				if task.Status == "in_progress" {
					task.Status = "needs_review"
				}
			}
			for _, cp := range plan.Document.Checkpoints {
				if cp.Status == "needs_review" && task.Status == "in_progress" {
					task.Status = "needs_review"
					break
				}
			}
		}
	}
}

// TaskRouteResult represents the synthesized plan, tier, and workspace scope for a task.
type TaskRouteResult = pebblestore.TaskRouteResult

func routeAndPlanProjectTask(prompt string, wsPath string, projectContext string, workspaces []pebblestore.ProjectWorkspaceRef, feedback string, lastError string) TaskRouteResult {
	return pebblestore.RouteAndPlanProjectTask(prompt, wsPath, projectContext, workspaces, feedback, lastError)
}

func truncateString(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// organizeTaskFromEnglishPrompt delegates to routeAndPlanProjectTask for backward compatibility.
func organizeTaskFromEnglishPrompt(prompt string, wsPath string) (title string, agent string, outcomeType string, branch string, mission string, stages []string, deliverables []pebblestore.ProjectTaskDeliverable) {
	routed := routeAndPlanProjectTask(prompt, wsPath, "", nil, "", "")
	return routed.Title, routed.Agent, routed.OutcomeType, routed.Branch, routed.Mission, routed.Stages, routed.Deliverables
}

// SynthesizeProjectContext reads AGENTS.md and README.md from the provided workspace paths
// and synthesizes an authoritative project.md document.
func SynthesizeProjectContext(projectName string, wsPaths []string) string {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		projectName = "Project"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s Architecture & Project Charter\n\n", projectName))
	sb.WriteString("## Bound Workspaces & Architecture\n")

	for _, p := range wsPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		base := filepath.Base(p)
		sb.WriteString(fmt.Sprintf("- **`%s`** (`%s`)\n", base, p))

		// Scan for AGENTS.md and README.md
		var docFound bool
		for _, docName := range []string{"AGENTS.md", "README.md"} {
			docPath := filepath.Join(p, docName)
			if fi, err := os.Stat(docPath); err == nil && !fi.IsDir() {
				if data, err := os.ReadFile(docPath); err == nil {
					lines := strings.Split(string(data), "\n")
					var extracted []string
					for _, l := range lines {
						trimmed := strings.TrimSpace(l)
						if trimmed == "" {
							continue
						}
						// Skip markdown headers
						if strings.HasPrefix(trimmed, "#") {
							continue
						}
						// Collect relevant instruction / architecture lines
						if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || len(trimmed) > 20 {
							extracted = append(extracted, trimmed)
							if len(extracted) >= 4 {
								break
							}
						}
					}
					if len(extracted) > 0 {
						docFound = true
						sb.WriteString(fmt.Sprintf("  - *Source (%s)*:\n", docName))
						for _, ex := range extracted {
							sb.WriteString(fmt.Sprintf("    > %s\n", ex))
						}
						break
					}
				}
			}
		}
		if !docFound {
			sb.WriteString("  - *Role*: General repository module.\n")
		}
	}

	sb.WriteString("\n## System Architecture & Integration Boundary\n")
	sb.WriteString("- Coordinated by Swarm Project Orchestrator (`system-orchestrator`).\n")
	sb.WriteString("- Delegated execution runs on isolated worktrees via `coder`, `designer`, `finder`, and `swarm` waves.\n")
	sb.WriteString("- Durable V3 session contracts and Pebble persistence.\n\n")
	sb.WriteString("## Operational Invariants\n")
	sb.WriteString("- Local-first operation; zero external credential leaks.\n")
	sb.WriteString("- Minimal high-craft diffs with automated parent verification.\n")
	sb.WriteString("- Review-first delivery: finished tasks transition to `needs_review` before completion.\n")

	return sb.String()
}

// deployProjectTaskExecution handles direct media generation for images/videos or
// creates and enqueues a canonical V3 session with compiled agent profile and RunIntent for agent tasks.
func (s *Server) deployProjectTaskExecution(p identity.Principal, proj *pebblestore.ProjectRecord, task *pebblestore.ProjectTaskRecord, taskStatus string, prompt string) error {
	if task == nil {
		return errors.New("task is required")
	}

	// 1. Direct Media Generation: image, generative video, and designer swarm tasks do NOT spin up chat agent sessions.
	// They directly generate media deliverables and transition to needs_review.
	isDirectMedia := task.Agent == "image" || task.Agent == "video" || (task.Agent == "designer" && (task.Tier == "swarm" || len(task.Deliverables) > 1 || task.OutcomeType == "media_bundle"))
	if isDirectMedia {
		task.SessionID = ""
		if taskStatus == "in_progress" {
			now := time.Now().UnixMilli()
			if task.Agent == "image" || task.Agent == "designer" {
				count := task.VariantCount
				if count <= 0 {
					count = len(task.Deliverables)
				}
				if count <= 0 {
					count = 1
				}
				ar := task.AspectRatio
				if ar == "" {
					ar = "1:1"
				}
				var delivs []pebblestore.ProjectTaskDeliverable
				for i := 1; i <= count; i++ {
					delivs = append(delivs, pebblestore.ProjectTaskDeliverable{
						ID:          fmt.Sprintf("deliv_img_%d_%d", now, i),
						Title:       fmt.Sprintf("%s (Variant %d, %s)", task.Title, i, ar),
						Kind:        "image",
						Status:      "generating",
						Description: fmt.Sprintf("Autonomous deliverable for %s in aspect ratio %s", task.Title, ar),
					})
				}
				task.Deliverables = delivs
				task.Status = "in_progress"
				task.ActionNeeded = fmt.Sprintf("Generating %d deliverable variant(s)...", count)
				task.WhatDidDo = []string{"Approved mission", "Generating media assets"}
			} else if task.Agent == "video" {
				ar := task.AspectRatio
				if ar == "" {
					ar = "16:9"
				}
				sceneCount := len(task.Scenes)
				if sceneCount == 0 {
					sceneCount = 2
				}
				soundtrack := task.Soundtrack
				if soundtrack == "" {
					soundtrack = "Ambient Electronic Beats"
				}
				var delivs []pebblestore.ProjectTaskDeliverable
				delivs = append(delivs, pebblestore.ProjectTaskDeliverable{
					ID:          fmt.Sprintf("deliv_vid_%d", now),
					Title:       fmt.Sprintf("%s (Video Story, %s)", task.Title, ar),
					Kind:        "video",
					Status:      "generating",
					Thumbnail:   "video",
					Duration:    fmt.Sprintf("%ds", sceneCount*4),
					Description: fmt.Sprintf("Compiled %d-scene video story with soundtrack (%s): %s", sceneCount, soundtrack, task.Title),
				})
				task.Deliverables = delivs
				task.Status = "in_progress"
				task.ActionNeeded = "Rendering video story sequence..."
				task.WhatDidDo = []string{"Approved mission", "Rendering video sequence"}
			}
			go s.executeDirectMediaTask(p, proj, task)
		}
		return nil
	}

	// 2. Agent Tasks: coder, finder, designer, swarm, plan.
	// Must create a canonical V3 session with compiled agent_profile, seed message, and RunIntent.
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

	mode := sessionruntime.ModeAuto
	targetAgent := strings.TrimSpace(task.Agent)
	if targetAgent == "plan" {
		mode = sessionruntime.ModePlan
		targetAgent = "swarm"
	}
	if targetAgent == "" {
		targetAgent = "swarm"
	}

	// Resolve canonical default Swarm preference for fallback or primary Swarm task
	var defaultSwarmPref pebblestore.ModelPreference
	if s.agentModelSettings != nil && p.AccountScopeID != "" {
		if settings, err := s.agentModelSettings.GetForAccount(p.AccountScopeID); err == nil {
			swarmAssignment := settings.Swarm.Action
			if mode == sessionruntime.ModePlan && strings.TrimSpace(settings.Swarm.Plan.Model) != "" {
				swarmAssignment = settings.Swarm.Plan
			}
			defaultSwarmPref = pebblestore.ModelPreference{
				Provider:    strings.TrimSpace(swarmAssignment.Provider),
				Model:       strings.TrimSpace(swarmAssignment.Model),
				Thinking:    strings.TrimSpace(swarmAssignment.Thinking),
				ServiceTier: strings.TrimSpace(swarmAssignment.ServiceTier),
				ContextMode: strings.TrimSpace(swarmAssignment.ContextMode),
			}
		}
	}
	if defaultSwarmPref.Provider == "" || defaultSwarmPref.Model == "" {
		if s.model != nil {
			if def, err := s.model.ResolvePreference(pebblestore.ModelPreference{}); err == nil {
				defaultSwarmPref = def.Preference
			}
		}
	}
	if defaultSwarmPref.Provider == "" || defaultSwarmPref.Model == "" {
		defaultSwarmPref = pebblestore.ModelPreference{
			Provider: "google",
			Model:    "gemini-3.8-flash",
			Thinking: "low",
		}
	}

	var resolvedPref pebblestore.ModelPreference
	var agentProfile pebblestore.AgentProfile
	var fallbackAlert string

	if canonicalID, isCanonical := agentruntime.CanonicalSystemAgentID(targetAgent); isCanonical && canonicalID != agentruntime.SwarmAgentID {
		// System subagent (coder, finder, designer, compact, etc.)
		// Must use canonical account-scoped agent-model settings service (agentmodel.ResolveSystemAgent)
		resolvedModel, profile, err := agentmodel.ResolveSystemAgent(s.model, s.agents, s.agentModelSettings, p.AccountScopeID, canonicalID, "")
		if err == nil && resolvedModel.Preference.Model != "" {
			resolvedPref = resolvedModel.Preference
			agentProfile = profile
		} else {
			// Resolution failed or not configured!
			// Must fall back to Swarm default AND warn in the task card!
			resolvedPref = defaultSwarmPref
			fallbackAlert = fmt.Sprintf("Configured model for agent %q could not be resolved (%v); fell back to Swarm default (%s/%s).", targetAgent, err, defaultSwarmPref.Provider, defaultSwarmPref.Model)
			if s.agents != nil {
				agentProfile, _ = s.agents.ResolveSystemAgent(canonicalID, pebblestore.AgentProfile{
					Provider:        defaultSwarmPref.Provider,
					Model:           defaultSwarmPref.Model,
					Thinking:        defaultSwarmPref.Thinking,
					AutoServiceTier: defaultSwarmPref.ServiceTier,
					ContextMode:     defaultSwarmPref.ContextMode,
				})
			}
		}
	} else if strings.EqualFold(targetAgent, "swarm") {
		// Swarm primary agent
		resolvedPref = defaultSwarmPref
		if s.agents != nil {
			agentProfile, _ = s.agents.ResolveSystemAgent(agentruntime.SwarmAgentID, pebblestore.AgentProfile{
				Provider:        defaultSwarmPref.Provider,
				Model:           defaultSwarmPref.Model,
				Thinking:        defaultSwarmPref.Thinking,
				AutoServiceTier: defaultSwarmPref.ServiceTier,
				ContextMode:     defaultSwarmPref.ContextMode,
			})
		}
	} else {
		// Custom / saved agent profile
		var profileFound bool
		if s.agents != nil {
			if p.AccountScopeID != "" {
				agentProfile, profileFound, _ = s.agents.GetProfileForAccount(p.AccountScopeID, targetAgent)
			}
			if !profileFound {
				agentProfile, profileFound, _ = s.agents.GetProfile(targetAgent)
			}
		}
		if profileFound && agentProfile.Model != "" {
			resolvedPref = pebblestore.ModelPreference{
				Provider:    agentProfile.Provider,
				Model:       agentProfile.Model,
				Thinking:    agentProfile.Thinking,
				ServiceTier: agentProfile.AutoServiceTier,
				ContextMode: agentProfile.ContextMode,
			}
		} else {
			resolvedPref = defaultSwarmPref
			fallbackAlert = fmt.Sprintf("Custom agent profile %q not found or has no model; fell back to Swarm default (%s/%s).", targetAgent, defaultSwarmPref.Provider, defaultSwarmPref.Model)
			if !profileFound && s.agents != nil {
				agentProfile, _ = s.agents.ResolveSystemAgent(agentruntime.SwarmAgentID, pebblestore.AgentProfile{
					Provider:        defaultSwarmPref.Provider,
					Model:           defaultSwarmPref.Model,
					Thinking:        defaultSwarmPref.Thinking,
					AutoServiceTier: defaultSwarmPref.ServiceTier,
					ContextMode:     defaultSwarmPref.ContextMode,
				})
			}
		}
	}

	if agentProfile.Name == "" {
		trueVal := true
		agentProfile = pebblestore.AgentProfile{
			Name:                targetAgent,
			Mode:                "primary",
			RuntimeMode:         pebblestore.AgentRuntimeModePlanAuto,
			DefaultSessionMode:  "auto",
			ExitPlanModeEnabled: &trueVal,
			Provider:            resolvedPref.Provider,
			Model:               resolvedPref.Model,
			Thinking:            resolvedPref.Thinking,
			Enabled:             true,
			ToolContract:        &pebblestore.AgentToolContract{Preset: "full"},
		}
	}

	if fallbackAlert != "" {
		if task.RouterAlert == "" {
			task.RouterAlert = fallbackAlert
		} else if !strings.Contains(task.RouterAlert, fallbackAlert) {
			task.RouterAlert = task.RouterAlert + " | " + fallbackAlert
		}
		task.WhatDidDo = append(task.WhatDidDo, fallbackAlert)
	}

	now := time.Now().UnixMilli()
	sessionID := sessionruntime.NewSessionID()
	metadata := map[string]any{
		"project_id":           task.ProjectID,
		"task_id":              task.ID,
		"task_title":           task.Title,
		"agent_name":           agentProfile.Name,
		"resolved_agent_name":  agentProfile.Name,
		"agent_mode":           agentProfile.Mode,
		"runtime_mode":         agentProfile.RuntimeMode,
		"default_session_mode": pebblestore.AgentProfileDefaultSessionMode(agentProfile),
		"agent_profile":        cloneSessionsV3AgentProfile(agentProfile),
		"role":                 "project_task",
		"workspaces_involved":  task.WorkspacesInvolved,
		"context_pool_summary": task.ContextPoolSummary,
		"plan_summary":         task.PlanSummary,
		"full_plan_markdown":   task.FullPlanMarkdown,
		"tier":                 task.Tier,
		"revision":             task.Revision,
	}
	if agentProfile.ExitPlanModeEnabled != nil {
		metadata["exit_plan_mode_enabled"] = *agentProfile.ExitPlanModeEnabled
	}
	if agentProfile.ToolContract != nil && agentProfile.ToolContract.Preset != "" {
		metadata["tool_contract_preset"] = agentProfile.ToolContract.Preset
	}
	if task.Agent == "plan" {
		metadata["default_session_mode"] = "plan"
	}

	worktreeBranch := strings.TrimSpace(task.WorktreeBranch)
	if worktreeBranch == "" || worktreeBranch == "main" || worktreeBranch == "dev" || worktreeBranch == "master" {
		worktreeBranch, _ = pebblestore.MakeWorktreeBranch(task.Title, prompt)
	}
	task.WorktreeBranch = worktreeBranch
	task.WorktreeName = strings.TrimPrefix(worktreeBranch, "agent/")
	task.WorktreeName = strings.TrimPrefix(task.WorktreeName, "worktree/")

	avail := true
	grants := []pebblestore.WorkspaceGrant{
		{Kind: pebblestore.WorkspaceGrantPrimary, Path: wsPath, Name: filepath.Base(wsPath), Available: &avail},
	}
	projName := "Project"
	if proj != nil && proj.Name != "" {
		projName = proj.Name
	}
	sessionSnapshot := pebblestore.SessionSnapshot{
		ID:              sessionID,
		UserID:          p.UserID,
		AccountScopeID:  p.AccountScopeID,
		WorkspacePath:   wsPath,
		WorkspaceName:   filepath.Base(wsPath),
		Title:           fmt.Sprintf("[%s] %s", projName, task.Title),
		Mode:            mode,
		Preference:      resolvedPref,
		Metadata:        metadata,
		WorkspaceGrants: grants,
		WorkspaceUsage:  pebblestore.WorkspaceUsageFromGrants(grants),
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if s.worktrees != nil && (targetAgent == "coder" || task.OutcomeType == "code_pr" || task.OutcomeType == "bug_patch") {
		if alloc, err := s.worktrees.AllocateDetachedWorkspaceRequestedForPrincipal(p, wsPath, sessionID, "", worktreeBranch); err == nil && alloc.WorkspacePath != "" {
			sessionSnapshot.WorktreeEnabled = true
			sessionSnapshot.WorktreeRootPath = strings.TrimSpace(alloc.WorkspacePath)
			sessionSnapshot.WorktreeBaseBranch = strings.TrimSpace(alloc.BaseBranch)
			sessionSnapshot.WorktreeBranch = strings.TrimSpace(alloc.BranchName)
			task.WorkspacePath = alloc.WorkspacePath
			task.WorktreeBranch = alloc.BranchName
			task.BaseBranch = alloc.BaseBranch
			task.BaseCommit = alloc.BaseCommit
			task.WorktreeName = strings.TrimPrefix(alloc.BranchName, "agent/")
			metadata["base_commit"] = alloc.BaseCommit
			metadata["swarm_v3_source_workspace_path"] = alloc.RepoRoot
			sessionSnapshot.Metadata = metadata
			available := true
			sessionSnapshot.WorkspaceGrants = append(sessionSnapshot.WorkspaceGrants, pebblestore.WorkspaceGrant{
				Kind: pebblestore.WorkspaceGrantWorktree, Path: alloc.WorkspacePath, Available: &available,
			})
			sessionSnapshot.WorkspaceUsage = pebblestore.WorkspaceUsageFromGrants(sessionSnapshot.WorkspaceGrants)
		}
	}

	createKey := fmt.Sprintf("project-task:create:%s:%d", sessionID, now)
	_, createErr := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          p.UserID,
		AccountScopeID:  p.AccountScopeID,
		ClientRequestID: createKey,
		IdempotencyKey:  createKey,
		PayloadHash:     createKey,
		RequestHash:     createKey,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &sessionSnapshot,
		NowUnixMs:       now,
	})
	if createErr != nil {
		return createErr
	}
	task.SessionID = sessionID

	var tr taskrouter.Service
	seedMsg := tr.BuildAgentSeedPrompt(task, proj)
	msgID := fmt.Sprintf("msg_%s_%d", sessionID, now)
	msg := pebblestore.MessageSnapshot{
		ID:             msgID,
		SessionID:      sessionID,
		UserID:         p.UserID,
		AccountScopeID: p.AccountScopeID,
		Role:           "user",
		Content:        seedMsg,
		Metadata: map[string]any{
			"role":                 "project_context_seed",
			"task_id":              task.ID,
			"project_id":           task.ProjectID,
			"context_pool_summary": task.ContextPoolSummary,
		},
		CreatedAt: now,
	}

	var runIntent *pebblestore.V3SessionRunIntent
	runID := ""
	if taskStatus == "in_progress" {
		runID = fmt.Sprintf("desktop-v3-run:%s", sessionruntime.NewSessionID())
		parentSessionID := ""
		if proj != nil {
			parentSessionID = proj.PrimarySessionID
		}
		runIntent = &pebblestore.V3SessionRunIntent{
			SessionID:       sessionID,
			RunID:           runID,
			EpochID:         "epoch-00000000000000000001",
			UserID:          p.UserID,
			AccountScopeID:  p.AccountScopeID,
			ParentSessionID: parentSessionID,
			SourceMessageID: msgID,
			Status:          pebblestore.V3RunIntentPendingExecutor,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
	}

	msgKey := fmt.Sprintf("project-task:seed:%s:%d", sessionID, now)
	_, appendErr := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
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
	if appendErr != nil {
		return appendErr
	}

	if taskStatus == "in_progress" && runIntent != nil {
		parentSessionID := ""
		if proj != nil {
			parentSessionID = proj.PrimarySessionID
		}
		s.EnqueueSessionRun(p, sessionID, runID, parentSessionID)
	}

	return nil
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := PrincipalFromRequest(r)
	if !ok || !p.Valid() {
		writeError(w, http.StatusUnauthorized, errors.New("trusted user required"))
		return
	}
	if p.Type != "user" {
		writeError(w, http.StatusForbidden, errors.New("explicit user required"))
		return
	}
	if s.sessions == nil || s.sessions.Store() == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("session service unavailable"))
		return
	}

	db := s.sessions.Store()
	rawPath := strings.TrimPrefix(r.URL.Path, ProjectsPath)
	trimmed := strings.Trim(rawPath, "/")

	// 1. Root /v3/projects
	if trimmed == "" {
		if r.Method == http.MethodGet {
			if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
				return
			}
			q := r.URL.Query()
			limit := 100
			if q.Get("limit") != "" {
				if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
					limit = n
				}
			}
			records, err := db.ListProjects(p.AccountScopeID, limit)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if records == nil {
				records = []pebblestore.ProjectRecord{}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"projects": records,
				"count":    len(records),
			})
			return
		}

		if r.Method == http.MethodPost {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			var rec pebblestore.ProjectRecord
			body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
				return
			}
			if err := json.Unmarshal(body, &rec); err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid JSON payload"))
				return
			}

			if err := db.PutProject(p.AccountScopeID, &rec); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}

			writeJSON(w, http.StatusCreated, map[string]any{
				"project": rec,
			})
			return
		}

		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}

	segments := strings.Split(trimmed, "/")

	// 2. Synthesize context: POST /v3/projects/synthesize-context
	if len(segments) == 1 && segments[0] == "synthesize-context" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:read", "sessions:read", "projects:write", "sessions:write") {
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
			return
		}
		var req struct {
			Name       string   `json:"name"`
			Workspaces []string `json:"workspaces"`
		}
		if len(body) > 0 {
			_ = json.Unmarshal(body, &req)
		}
		ctxText := SynthesizeProjectContext(req.Name, req.Workspaces)
		writeJSON(w, http.StatusOK, map[string]any{
			"project_context": ctxText,
		})
		return
	}

	projectID := segments[0]

	// 3. Single project resource: /v3/projects/{id}
	if len(segments) == 1 {
		if r.Method == http.MethodGet {
			if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
				return
			}
			rec, found, err := db.GetProject(p.AccountScopeID, projectID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if !found || rec == nil {
				writeError(w, http.StatusNotFound, errors.New("project not found"))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"project": rec,
			})
			return
		}

		if r.Method == http.MethodPatch {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
				return
			}
			var patch map[string]any
			if err := json.Unmarshal(body, &patch); err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid JSON payload"))
				return
			}

			updated, err := db.UpdateProject(p.AccountScopeID, projectID, func(p *pebblestore.ProjectRecord) error {
				if v, ok := patch["name"].(string); ok && strings.TrimSpace(v) != "" {
					p.Name = strings.TrimSpace(v)
				}
				if v, ok := patch["description"].(string); ok {
					p.Description = strings.TrimSpace(v)
				}
				if v, ok := patch["project_context"].(string); ok {
					p.ProjectContext = v
				}
				if v, ok := patch["primary_session_id"].(string); ok {
					p.PrimarySessionID = strings.TrimSpace(v)
				}
				if workspacesRaw, ok := patch["workspaces"]; ok {
					rawBytes, err := json.Marshal(workspacesRaw)
					if err == nil {
						var ws []pebblestore.ProjectWorkspaceRef
						if err := json.Unmarshal(rawBytes, &ws); err == nil {
							p.Workspaces = ws
						}
					}
				}
				if tasksRaw, ok := patch["active_task_ids"]; ok {
					rawBytes, err := json.Marshal(tasksRaw)
					if err == nil {
						var tasks []string
						if err := json.Unmarshal(rawBytes, &tasks); err == nil {
							p.ActiveTaskIDs = tasks
						}
					}
				}
				if autosRaw, ok := patch["automation_ids"]; ok {
					rawBytes, err := json.Marshal(autosRaw)
					if err == nil {
						var autos []string
						if err := json.Unmarshal(rawBytes, &autos); err == nil {
							p.AutomationIDs = autos
						}
					}
				}
				return nil
			})
			if err != nil {
				if strings.Contains(err.Error(), "not found") {
					writeError(w, http.StatusNotFound, err)
					return
				}
				writeError(w, http.StatusBadRequest, err)
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"project": updated,
			})
			return
		}

		if r.Method == http.MethodDelete {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			if err := db.DeleteProject(p.AccountScopeID, projectID); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "deleted",
				"id":     projectID,
			})
			return
		}

		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}

	// 3.4 Clear orchestrator context: POST /v3/projects/{id}/orchestrator:clear-context or /v3/projects/{id}/clear-context
	if (len(segments) == 2 && (segments[1] == "orchestrator:clear-context" || segments[1] == "clear-context" || segments[1] == "orchestrator-clear-context")) || (len(segments) == 3 && segments[1] == "orchestrator" && segments[2] == "clear-context") {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		proj, found, err := db.GetProject(p.AccountScopeID, projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || proj == nil {
			writeError(w, http.StatusNotFound, errors.New("project not found"))
			return
		}

		oldSessionID := strings.TrimSpace(proj.PrimarySessionID)
		if oldSessionID != "" {
			_, _ = s.sessions.ArchiveSessionWithEvent(oldSessionID)
		}

		repoPath := ""
		if len(proj.Workspaces) > 0 {
			repoPath = proj.Workspaces[0].Path
		}
		if repoPath == "" {
			repoPath = "."
		}

		newSessionID := sessionruntime.NewSessionID()
		now := time.Now().UnixMilli()
		createKey := fmt.Sprintf("project-orchestrator:reset:%s:%d", newSessionID, now)

		// 1. Resolve canonical Swarm plan agent model preference
		var planPref pebblestore.ModelPreference
		if s.agentModelSettings != nil && p.AccountScopeID != "" {
			if settings, err := s.agentModelSettings.GetForAccount(p.AccountScopeID); err == nil {
				planAssignment := settings.Swarm.Plan
				if strings.TrimSpace(planAssignment.Model) == "" {
					planAssignment = settings.Swarm.Action
				}
				planPref = pebblestore.ModelPreference{
					Provider:    strings.TrimSpace(planAssignment.Provider),
					Model:       strings.TrimSpace(planAssignment.Model),
					Thinking:    strings.TrimSpace(planAssignment.Thinking),
					ServiceTier: strings.TrimSpace(planAssignment.ServiceTier),
					ContextMode: strings.TrimSpace(planAssignment.ContextMode),
				}
			}
		}
		if planPref.Provider == "" || planPref.Model == "" {
			if s.model != nil {
				if def, err := s.model.ResolvePreference(pebblestore.ModelPreference{}); err == nil {
					planPref = def.Preference
				}
			}
		}
		if planPref.Provider == "" || planPref.Model == "" {
			planPref = pebblestore.ModelPreference{
				Provider: "google",
				Model:    "gemini-3.8-flash",
				Thinking: "low",
			}
		}

		// 2. Resolve primary Swarm ID and binding
		var primarySwarmID string
		if localNode, localOK, lErr := s.swarmLocalNode(); lErr == nil && localOK {
			primarySwarmID = strings.TrimSpace(localNode.SwarmID)
		}
		binding, bErr := s.resolveSessionsV3PrimaryBinding(p, sessionsV3CreateRequest{
			WorkspacePath: repoPath,
		})
		if bErr != nil {
			binding = sessionsV3PrimaryBinding{
				RuntimeSwarmID:       primarySwarmID,
				SourceWorkspacePath:  repoPath,
				SourceWorkspaceName:  proj.Name,
				RuntimeWorkspacePath: repoPath,
			}
		}
		if binding.RuntimeSwarmID == "" {
			binding.RuntimeSwarmID = primarySwarmID
		}

		// 3. Resolve system-orchestrator agent profile with plan model
		baseOrchProfile := agentruntime.SwarmOrchestratorAgentProfileForContext(pebblestore.AgentProfile{
			Provider:        planPref.Provider,
			Model:           planPref.Model,
			Thinking:        planPref.Thinking,
			AutoServiceTier: planPref.ServiceTier,
			ContextMode:     planPref.ContextMode,
		})
		baseOrchProfile.Provider = planPref.Provider
		baseOrchProfile.Model = planPref.Model
		baseOrchProfile.Thinking = planPref.Thinking
		baseOrchProfile.AutoServiceTier = planPref.ServiceTier
		baseOrchProfile.ContextMode = planPref.ContextMode

		resolvedAgent := sessionsV3ResolvedAgentIdentity{
			Name:                baseOrchProfile.Name,
			ResolvedName:        baseOrchProfile.Name,
			Mode:                baseOrchProfile.Mode,
			RuntimeMode:         baseOrchProfile.RuntimeMode,
			ExitPlanModeEnabled: baseOrchProfile.ExitPlanModeEnabled != nil && *baseOrchProfile.ExitPlanModeEnabled,
			Profile:             baseOrchProfile,
		}
		if compiledAgent, cErr := s.resolveSessionsV3PrimaryCreateAgent(p, agentruntime.SwarmOrchestratorAgentID); cErr == nil {
			resolvedAgent = compiledAgent
			resolvedAgent.Profile.Provider = planPref.Provider
			resolvedAgent.Profile.Model = planPref.Model
			resolvedAgent.Profile.Thinking = planPref.Thinking
			resolvedAgent.Profile.AutoServiceTier = planPref.ServiceTier
			resolvedAgent.Profile.ContextMode = planPref.ContextMode
		}

		// 4. Resolve account default model profile snapshot
		var modelProfileSnapshot *pebblestore.SessionModelProfileSnapshot
		if snap, err := s.sessionModelProfileSnapshotFromAccountDefault(r.Context(), now); err == nil {
			modelProfileSnapshot = snap
		}

		// 5. Build full canonical server metadata
		baseMeta := map[string]any{
			"project_id": proj.ID,
			"role":       "project_orchestrator",
		}
		serverMeta := sessionsV3CreateServerMetadata(baseMeta, resolvedAgent, binding)
		metadata := sessionsV3ModelProfileMetadata(serverMeta, modelProfileSnapshot)
		metadata["agent_profile"] = cloneSessionsV3AgentProfile(resolvedAgent.Profile)
		if binding.RuntimeSwarmID != "" {
			metadata["swarm_v3_runtime_swarm_id"] = binding.RuntimeSwarmID
			metadata["swarm_v3_authority_host_swarm_id"] = binding.RuntimeSwarmID
		}

		avail := true
		grants := []pebblestore.WorkspaceGrant{
			{Kind: pebblestore.WorkspaceGrantPrimary, Path: repoPath, Name: proj.Name, Available: &avail},
		}

		sessionSnapshot := pebblestore.SessionSnapshot{
			ID:              newSessionID,
			UserID:          p.UserID,
			AccountScopeID:  p.AccountScopeID,
			Title:           fmt.Sprintf("Project Orchestrator: %s", proj.Name),
			WorkspacePath:   repoPath,
			WorkspaceName:   proj.Name,
			WorkspaceGrants: grants,
			WorkspaceUsage:  pebblestore.WorkspaceUsageFromGrants(grants),
			Mode:            sessionruntime.ModeAuto,
			Preference:      planPref,
			ModelProfile:    modelProfileSnapshot,
			Metadata:        metadata,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if profilePref, ok := sessionsV3ProfilePreference(sessionSnapshot); ok {
			sessionSnapshot.Preference = normalizeSessionsV3ModelPreference(profilePref)
		}
		_, createErr := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
			SessionID:       newSessionID,
			UserID:          p.UserID,
			AccountScopeID:  p.AccountScopeID,
			ClientRequestID: createKey,
			IdempotencyKey:  createKey,
			PayloadHash:     createKey,
			RequestHash:     createKey,
			Kind:            sessionruntime.SessionMutationCreateSession,
			Session:         &sessionSnapshot,
			NowUnixMs:       now,
		})
		if createErr != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("create orchestrator session: %w", createErr))
			return
		}

		proj.PrimarySessionID = newSessionID
		if err := db.PutProject(p.AccountScopeID, proj); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":                  true,
			"session_id":          newSessionID,
			"previous_session_id": oldSessionID,
			"project":             proj,
		})
		return
	}

	// 3.5 Media sub-resource: /v3/projects/{id}/media and /v3/projects/{id}/media/{media_id}
	if len(segments) >= 2 && segments[1] == "media" {
		proj, found, err := db.GetProject(p.AccountScopeID, projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || proj == nil {
			writeError(w, http.StatusNotFound, errors.New("project not found"))
			return
		}

		if len(segments) == 2 {
			if r.Method == http.MethodGet {
				if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
					return
				}
				list := proj.UploadedMedia
				if list == nil {
					list = []pebblestore.ProjectTaskMediaRef{}
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"media": list,
					"count": len(list),
				})
				return
			}
			if r.Method == http.MethodPost {
				if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
					return
				}
				body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
				if err != nil {
					writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
					return
				}
				var item pebblestore.ProjectTaskMediaRef
				if err := json.Unmarshal(body, &item); err != nil {
					writeError(w, http.StatusBadRequest, errors.New("invalid JSON payload"))
					return
				}
				if strings.TrimSpace(item.ID) == "" {
					item.ID = fmt.Sprintf("med_%d", time.Now().UnixNano())
				}
				if item.CreatedAt == 0 {
					item.CreatedAt = time.Now().UnixMilli()
				}
				var updatedList []pebblestore.ProjectTaskMediaRef
				_, err = db.UpdateProject(p.AccountScopeID, projectID, func(p *pebblestore.ProjectRecord) error {
					p.UploadedMedia = append(p.UploadedMedia, item)
					updatedList = p.UploadedMedia
					return nil
				})
				if err != nil {
					writeError(w, http.StatusInternalServerError, err)
					return
				}
				writeJSON(w, http.StatusCreated, map[string]any{
					"media":          item,
					"uploaded_media": updatedList,
				})
				return
			}
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}

		if len(segments) == 3 {
			mediaID := segments[2]
			if r.Method == http.MethodDelete {
				if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
					return
				}
				var updatedList []pebblestore.ProjectTaskMediaRef
				_, err = db.UpdateProject(p.AccountScopeID, projectID, func(p *pebblestore.ProjectRecord) error {
					for _, m := range p.UploadedMedia {
						if m.ID != mediaID {
							updatedList = append(updatedList, m)
						}
					}
					p.UploadedMedia = updatedList
					return nil
				})
				if err != nil {
					writeError(w, http.StatusInternalServerError, err)
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"removed":        true,
					"media_id":       mediaID,
					"uploaded_media": updatedList,
				})
				return
			}
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
	}

	// 4. Project Tasks collection: /v3/projects/{id}/tasks
	if len(segments) == 2 && segments[1] == "tasks" {
		if r.Method == http.MethodGet {
			if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
				return
			}
			tasks, err := db.ListProjectTasks(p.AccountScopeID, projectID, 100)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if tasks == nil {
				tasks = []pebblestore.ProjectTaskRecord{}
			}
			for i := range tasks {
				gitState := inspectTaskGitState(tasks[i], db)
				tasks[i].WorktreeBranch = gitState.worktreeBranch
				tasks[i].WorktreeName = gitState.worktreeName
				tasks[i].BaseBranch = gitState.baseBranch
				tasks[i].UnintegratedCommits = gitState.unintegratedCommits
				tasks[i].BehindCommits = gitState.behindCommits
				tasks[i].IsIntegrated = gitState.isIntegrated
				tasks[i].DiffSummary = gitState.diffSummary
				tasks[i].IsDirty = gitState.isDirty
				tasks[i].DirtyCount = gitState.dirtyCount
				tasks[i].SyncWarning = gitState.syncWarning
				if gitState.actionNeeded != "" {
					tasks[i].ActionNeeded = gitState.actionNeeded
				}
				origStatus := tasks[i].Status
				syncTaskSessionState(&tasks[i], db)
				hydrateTaskProgramStatus(&tasks[i], db)
				if tasks[i].Status != origStatus {
					_ = db.PutProjectTask(p.AccountScopeID, &tasks[i])
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"tasks": tasks,
				"count": len(tasks),
			})
			return
		}

		if r.Method == http.MethodPost {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
				return
			}
			var req struct {
				Title               string                               `json:"title"`
				Description         string                               `json:"description,omitempty"`
				Status              string                               `json:"status,omitempty"`
				Agent               string                               `json:"agent,omitempty"`
				WorkerName          string                               `json:"worker_name,omitempty"`
				OutcomeType         string                               `json:"outcome_type,omitempty"`
				WorkspacePath       string                               `json:"workspace_path,omitempty"`
				WorktreeBranch      string                               `json:"worktree_branch,omitempty"`
				UnintegratedCommits int                                  `json:"unintegrated_commits,omitempty"`
				DiffSummary         string                               `json:"diff_summary,omitempty"`
				IsDirty             bool                                 `json:"is_dirty,omitempty"`
				ActionNeeded        string                               `json:"action_needed,omitempty"`
				WhatDidDo           []string                             `json:"what_did_do,omitempty"`
				WhatNotDone         []string                             `json:"what_not_done,omitempty"`
				PipelineStages      []string                             `json:"pipeline_stages,omitempty"`
				CurrentStageIndex   int                                  `json:"current_stage_index,omitempty"`
				Deliverables        []pebblestore.ProjectTaskDeliverable `json:"deliverables,omitempty"`
				WorkspacesInvolved  []string                             `json:"workspaces_involved,omitempty"`
				PlanSummary         string                               `json:"plan_summary,omitempty"`
				FullPlanMarkdown    string                               `json:"full_plan_markdown,omitempty"`
				Tier                string                               `json:"tier,omitempty"`
				Revision            int                                  `json:"revision,omitempty"`
				LastError           string                               `json:"last_error,omitempty"`
				DeploySession       bool                                 `json:"deploy_session,omitempty"`
				Prompt              string                               `json:"prompt,omitempty"`
				Intent              string                               `json:"intent,omitempty"`
				AspectRatio         string                               `json:"aspect_ratio,omitempty"`
				VariantCount        int                                  `json:"variant_count,omitempty"`
				DeliverableCount    int                                  `json:"deliverable_count,omitempty"`
				ScenesCount         int                                  `json:"scenes_count,omitempty"`
				Soundtrack          string                               `json:"soundtrack,omitempty"`
				AutoApprove         bool                                 `json:"auto_approve,omitempty"`
				AttachedMedia       []pebblestore.ProjectTaskMediaRef    `json:"attached_media,omitempty"`
				TaskProgram         *pebblestore.TaskProgramDefinition   `json:"task_program,omitempty"`
				TaskProgramID       string                               `json:"task_program_id,omitempty"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid JSON payload"))
				return
			}

			proj, found, err := db.GetProject(p.AccountScopeID, projectID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if !found || proj == nil {
				writeError(w, http.StatusNotFound, errors.New("project not found"))
				return
			}

			prompt := strings.TrimSpace(req.Prompt)
			if prompt == "" && strings.TrimSpace(req.Title) != "" {
				prompt = strings.TrimSpace(req.Title)
			}

			taskRouter := taskrouter.NewService(func(ctx context.Context, instructions, input string) (string, error) {
				res, err := s.invokeConfiguredRouterOnce(ctx, p, instructions, input, 64<<10)
				if err != nil {
					return "", err
				}
				return res.Text, nil
			})
			if req.VariantCount <= 0 && req.DeliverableCount > 0 {
				req.VariantCount = req.DeliverableCount
			}
			routed := taskRouter.RouteTask(r.Context(), taskrouter.TaskRouteOptions{
				Prompt:             prompt,
				RequestedWorkspace: req.WorkspacePath,
				Intent:             req.Intent,
				AspectRatio:        req.AspectRatio,
				VariantCount:       req.VariantCount,
				ScenesCount:        req.ScenesCount,
				Soundtrack:         req.Soundtrack,
				AutoApprove:        req.AutoApprove,
				AttachedMedia:      req.AttachedMedia,
				Project:            proj,
			})

			title := strings.TrimSpace(req.Title)
			if title == "" {
				title = routed.Title
			}
			agentName := strings.TrimSpace(req.Agent)
			if agentName == "" {
				agentName = routed.Agent
			}
			outcomeType := strings.TrimSpace(req.OutcomeType)
			if outcomeType == "" {
				outcomeType = routed.OutcomeType
			}
			worktreeBranch := strings.TrimSpace(req.WorktreeBranch)
			if worktreeBranch == "" || worktreeBranch == "main" || worktreeBranch == "dev" || worktreeBranch == "master" {
				worktreeBranch = routed.Branch
			}
			if worktreeBranch == "" || worktreeBranch == "main" || worktreeBranch == "dev" || worktreeBranch == "master" {
				worktreeBranch, _ = pebblestore.MakeWorktreeBranch(title, prompt)
			}
			worktreeName := strings.TrimPrefix(worktreeBranch, "agent/")
			worktreeName = strings.TrimPrefix(worktreeName, "worktree/")
			description := strings.TrimSpace(req.Description)
			if description == "" {
				description = routed.Mission
			}
			stages := req.PipelineStages
			if len(stages) == 0 {
				stages = routed.Stages
			}
			deliverables := req.Deliverables
			if len(deliverables) == 0 {
				deliverables = routed.Deliverables
			}
			workspacesInvolved := req.WorkspacesInvolved
			if len(workspacesInvolved) == 0 {
				workspacesInvolved = routed.WorkspacesInvolved
			}
			planSummary := strings.TrimSpace(req.PlanSummary)
			if planSummary == "" {
				planSummary = routed.PlanSummary
			}
			fullPlanMarkdown := strings.TrimSpace(req.FullPlanMarkdown)
			if fullPlanMarkdown == "" {
				fullPlanMarkdown = routed.FullPlanMarkdown
			}
			tier := strings.TrimSpace(req.Tier)
			if tier == "" {
				tier = routed.Tier
			}
			revision := req.Revision
			if revision <= 0 {
				revision = 1
			}
			workerName := strings.TrimSpace(req.WorkerName)
			if workerName == "" {
				workerName = fmt.Sprintf("@%s Worker", strings.Title(agentName))
			}

			taskStatus := strings.TrimSpace(req.Status)
			if taskStatus == "" {
				if req.AutoApprove {
					taskStatus = "in_progress"
				} else {
					taskStatus = "pending_approval"
				}
			}

			task := pebblestore.ProjectTaskRecord{
				ProjectID:           projectID,
				Title:               title,
				Description:         description,
				Status:              taskStatus,
				Agent:               agentName,
				WorkerName:          workerName,
				OutcomeType:         outcomeType,
				WorkspacePath:       strings.TrimSpace(req.WorkspacePath),
				WorktreeBranch:      worktreeBranch,
				WorktreeName:        worktreeName,
				BaseBranch:          "dev",
				UnintegratedCommits: req.UnintegratedCommits,
				DiffSummary:         strings.TrimSpace(req.DiffSummary),
				IsDirty:             req.IsDirty,
				ActionNeeded:        strings.TrimSpace(req.ActionNeeded),
				WhatDidDo:           req.WhatDidDo,
				WhatNotDone:         req.WhatNotDone,
				PipelineStages:      stages,
				CurrentStageIndex:   req.CurrentStageIndex,
				Deliverables:        deliverables,
				WorkspacesInvolved:  workspacesInvolved,
				ContextPoolSummary:  routed.ContextPoolSummary,
				PlanSummary:         planSummary,
				FullPlanMarkdown:    fullPlanMarkdown,
				Tier:                tier,
				Revision:            revision,
				LastError:           strings.TrimSpace(req.LastError),
				AspectRatio:         routed.AspectRatio,
				VariantCount:        routed.VariantCount,
				Scenes:              routed.Scenes,
				Soundtrack:          routed.Soundtrack,
				AutoApprove:         req.AutoApprove,
				RouterAlert:         routed.RouterAlert,
				AttachedMedia:       req.AttachedMedia,
				TaskProgram:         req.TaskProgram,
				TaskProgramID:       req.TaskProgramID,
			}
			if task.TaskProgram == nil && routed.TaskProgram != nil {
				task.TaskProgram = routed.TaskProgram
			}
			if task.Agent == "coder" || task.OutcomeType == "code_pr" || task.OutcomeType == "bug_patch" {
				task.AspectRatio = ""
				task.VariantCount = 0
			}
			if task.Status == "pending_approval" || task.Status == "planning" || task.Status == "queued" {
				task.UnintegratedCommits = 0
				task.BehindCommits = 0
				task.DiffSummary = ""
				task.IsDirty = false
				task.DirtyCount = 0
				task.IsIntegrated = false
				task.SyncWarning = ""
				task.ActionNeeded = ""
			}
			if len(task.AttachedMedia) == 0 && len(routed.AttachedMedia) > 0 {
				task.AttachedMedia = routed.AttachedMedia
			}

			// Deploy execution: Task Program standalone execution, direct media generation, or V3 session
			if task.TaskProgram != nil && (taskStatus == "in_progress" || req.AutoApprove) {
				task.Status = "in_progress"
				_ = s.deployProjectTaskProgram(p, proj, &task)
			} else {
				_ = s.deployProjectTaskExecution(p, proj, &task, taskStatus, prompt)
			}

			if err := db.PutProjectTask(p.AccountScopeID, &task); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}

			// Add task ID to project.ActiveTaskIDs
			_, _ = db.UpdateProject(p.AccountScopeID, projectID, func(p *pebblestore.ProjectRecord) error {
				for _, tid := range p.ActiveTaskIDs {
					if tid == task.ID {
						return nil
					}
				}
				p.ActiveTaskIDs = append(p.ActiveTaskIDs, task.ID)
				return nil
			})

			writeJSON(w, http.StatusCreated, map[string]any{
				"task": task,
			})
			return
		}

		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}

	// 5. Single Project Task resource: /v3/projects/{id}/tasks/{taskId}
	if len(segments) == 3 && segments[1] == "tasks" {
		taskID := segments[2]
		if r.Method == http.MethodGet {
			if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
				return
			}
			task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if !found || task == nil {
				writeError(w, http.StatusNotFound, errors.New("project task not found"))
				return
			}
			gitState := inspectTaskGitState(*task, db)
			task.WorktreeBranch = gitState.worktreeBranch
			task.WorktreeName = gitState.worktreeName
			task.BaseBranch = gitState.baseBranch
			task.UnintegratedCommits = gitState.unintegratedCommits
			task.BehindCommits = gitState.behindCommits
			task.IsIntegrated = gitState.isIntegrated
			task.DiffSummary = gitState.diffSummary
			task.IsDirty = gitState.isDirty
			task.DirtyCount = gitState.dirtyCount
			task.SyncWarning = gitState.syncWarning
			if gitState.actionNeeded != "" {
				task.ActionNeeded = gitState.actionNeeded
			}
			origStatus := task.Status
			syncTaskSessionState(task, db)
			hydrateTaskProgramStatus(task, db)
			if task.Status != origStatus {
				_ = db.PutProjectTask(p.AccountScopeID, task)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"task": task,
			})
			return
		}

		if r.Method == http.MethodPatch {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
				return
			}
			var patch map[string]any
			if err := json.Unmarshal(body, &patch); err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid JSON payload"))
				return
			}

			updated, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
				if v, ok := patch["title"].(string); ok && strings.TrimSpace(v) != "" {
					t.Title = strings.TrimSpace(v)
				}
				if v, ok := patch["description"].(string); ok {
					t.Description = strings.TrimSpace(v)
				}
				if v, ok := patch["status"].(string); ok && strings.TrimSpace(v) != "" {
					t.Status = strings.TrimSpace(v)
				}
				if v, ok := patch["agent"].(string); ok {
					t.Agent = strings.TrimSpace(v)
				}
				if v, ok := patch["worker_name"].(string); ok {
					t.WorkerName = strings.TrimSpace(v)
				}
				if v, ok := patch["current_stage_index"].(float64); ok {
					t.CurrentStageIndex = int(v)
				}
				if stagesRaw, ok := patch["pipeline_stages"]; ok {
					rawBytes, err := json.Marshal(stagesRaw)
					if err == nil {
						var stages []string
						if err := json.Unmarshal(rawBytes, &stages); err == nil {
							t.PipelineStages = stages
						}
					}
				}
				if delivRaw, ok := patch["deliverables"]; ok {
					rawBytes, err := json.Marshal(delivRaw)
					if err == nil {
						var delivs []pebblestore.ProjectTaskDeliverable
						if err := json.Unmarshal(rawBytes, &delivs); err == nil {
							t.Deliverables = delivs
						}
					}
				}
				if v, ok := patch["outcome_type"].(string); ok {
					t.OutcomeType = strings.TrimSpace(v)
				}
				if v, ok := patch["workspace_path"].(string); ok {
					t.WorkspacePath = strings.TrimSpace(v)
				}
				if v, ok := patch["worktree_branch"].(string); ok {
					t.WorktreeBranch = strings.TrimSpace(v)
				}
				if v, ok := patch["worktree_name"].(string); ok {
					t.WorktreeName = strings.TrimSpace(v)
				}
				if v, ok := patch["base_branch"].(string); ok {
					t.BaseBranch = strings.TrimSpace(v)
				}
				if v, ok := patch["behind_commits"].(float64); ok {
					t.BehindCommits = int(v)
				}
				if v, ok := patch["is_integrated"].(bool); ok {
					t.IsIntegrated = v
				}
				if v, ok := patch["dirty_count"].(float64); ok {
					t.DirtyCount = int(v)
				}
				if v, ok := patch["sync_warning"].(string); ok {
					t.SyncWarning = strings.TrimSpace(v)
				}
				if v, ok := patch["unintegrated_commits"].(float64); ok {
					t.UnintegratedCommits = int(v)
				}
				if v, ok := patch["diff_summary"].(string); ok {
					t.DiffSummary = strings.TrimSpace(v)
				}
				if v, ok := patch["is_dirty"].(bool); ok {
					t.IsDirty = v
				}
				if v, ok := patch["action_needed"].(string); ok {
					t.ActionNeeded = strings.TrimSpace(v)
				}
				if arr, ok := patch["what_did_do"].([]any); ok {
					var items []string
					for _, item := range arr {
						if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
							items = append(items, strings.TrimSpace(s))
						}
					}
					t.WhatDidDo = items
				}
				if arr, ok := patch["what_not_done"].([]any); ok {
					var items []string
					for _, item := range arr {
						if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
							items = append(items, strings.TrimSpace(s))
						}
					}
					t.WhatNotDone = items
				}
				if wsRaw, ok := patch["workspaces_involved"]; ok {
					rawBytes, err := json.Marshal(wsRaw)
					if err == nil {
						var ws []string
						if err := json.Unmarshal(rawBytes, &ws); err == nil {
							t.WorkspacesInvolved = ws
						}
					}
				}
				if v, ok := patch["plan_summary"].(string); ok {
					t.PlanSummary = strings.TrimSpace(v)
				}
				if v, ok := patch["full_plan_markdown"].(string); ok {
					t.FullPlanMarkdown = strings.TrimSpace(v)
				}
				if v, ok := patch["tier"].(string); ok {
					t.Tier = strings.TrimSpace(v)
				}
				if v, ok := patch["last_error"].(string); ok {
					t.LastError = strings.TrimSpace(v)
				}
				if v, ok := patch["revision"].(float64); ok {
					t.Revision = int(v)
				}
				if tpRaw, ok := patch["task_program"]; ok {
					rawBytes, err := json.Marshal(tpRaw)
					if err == nil {
						var tp pebblestore.TaskProgramDefinition
						if err := json.Unmarshal(rawBytes, &tp); err == nil {
							t.TaskProgram = &tp
						}
					}
				}
				if v, ok := patch["task_program_id"].(string); ok {
					t.TaskProgramID = strings.TrimSpace(v)
				}
				return nil
			})
			if err != nil {
				if strings.Contains(err.Error(), "not found") {
					writeError(w, http.StatusNotFound, err)
					return
				}
				writeError(w, http.StatusBadRequest, err)
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"task": updated,
			})
			return
		}

		if r.Method == http.MethodDelete {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			if err := db.DeleteProjectTask(p.AccountScopeID, projectID, taskID); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"status":  "deleted",
				"task_id": taskID,
			})
			return
		}

		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}

	// 6. Integrate task commits: POST /v3/projects/{id}/tasks/{taskId}/integrate
	if len(segments) == 4 && segments[1] == "tasks" && segments[3] == "integrate" {
		taskID := segments[2]
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || task == nil {
			writeError(w, http.StatusNotFound, errors.New("task not found"))
			return
		}

		// Inspect git state first
		gitState := inspectTaskGitState(*task, db)
		if gitState.isDirty {
			writeError(w, http.StatusConflict, fmt.Errorf("cannot integrate: worktree has %d uncommitted modification(s); commit or discard them before integrating", gitState.dirtyCount))
			return
		}
		if gitState.unintegratedCommits == 0 {
			if gitState.isIntegrated {
				writeJSON(w, http.StatusOK, map[string]any{
					"status":  "already_integrated",
					"message": fmt.Sprintf("Changes from %s are already integrated into %s", gitState.worktreeBranch, gitState.baseBranch),
					"task":    task,
				})
				return
			}
			writeError(w, http.StatusBadRequest, errors.New("cannot integrate: worktree has no commits to integrate"))
			return
		}

		// Resolve worktree target path
		targetPath := strings.TrimSpace(task.WorkspacePath)
		var sess *pebblestore.SessionSnapshot
		if task.SessionID != "" {
			if sSnap, sFound, _ := db.GetSession(task.SessionID); sFound {
				sess = &sSnap
				if sess.WorktreeEnabled && strings.TrimSpace(sess.WorktreeRootPath) != "" {
					targetPath = strings.TrimSpace(sess.WorktreeRootPath)
				}
			}
		}
		parentWs := ""
		if sess != nil && sess.Metadata != nil {
			if src, ok := sess.Metadata["swarm_v3_source_workspace_path"].(string); ok && src != "" {
				parentWs = src
			}
		}
		if parentWs == "" {
			proj, _, _ := db.GetProject(p.AccountScopeID, projectID)
			if proj != nil && len(proj.Workspaces) > 0 {
				parentWs = proj.Workspaces[0].Path
			}
		}
		if parentWs == "" {
			parentWs = task.WorkspacePath
		}

		worktreeSvc := &worktreeruntime.Service{}
		parentState, err := worktreeSvc.InspectTaskWorkspace(parentWs)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("inspect parent repository %q: %w", parentWs, err))
			return
		}
		if !parentState.Clean {
			writeError(w, http.StatusConflict, fmt.Errorf("parent repository %q is dirty (%s); commit or stash changes before integrating", parentWs, parentState.BranchName))
			return
		}

		childState, err := worktreeSvc.InspectTaskWorkspace(targetPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("inspect worktree %q: %w", targetPath, err))
			return
		}
		if !childState.Clean {
			writeError(w, http.StatusConflict, fmt.Errorf("worktree %q is dirty; commit changes before integrating", targetPath))
			return
		}

		baseCommit := gitState.baseCommit
		if baseCommit == "" {
			baseCommit = task.BaseCommit
		}
		if baseCommit == "" {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmdMb := exec.CommandContext(ctx, "git", "-C", targetPath, "merge-base", parentState.HeadCommit, childState.HeadCommit)
			if out, mbErr := cmdMb.Output(); mbErr == nil && len(bytes.TrimSpace(out)) > 0 {
				baseCommit = strings.TrimSpace(string(out))
			}
		}
		if baseCommit == "" || baseCommit == childState.HeadCommit {
			writeError(w, http.StatusBadRequest, errors.New("cannot integrate: worktree has no commits beyond its base commit"))
			return
		}

		plan, err := worktreeSvc.PrepareTaskIntegration(parentWs, parentState.BranchName, parentState.HeadCommit, []worktreeruntime.TaskIntegrationChild{
			{
				SessionID:  task.SessionID,
				BaseCommit: baseCommit,
				HeadCommit: childState.HeadCommit,
			},
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("prepare integration failed: %w", err))
			return
		}

		result, err := worktreeSvc.ApplyTaskIntegration(parentWs, plan)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("apply integration failed: %w", err))
			return
		}

		headDisplay := result.ResultingParentHead
		if len(headDisplay) > 8 {
			headDisplay = headDisplay[:8]
		}

		updated, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			t.Status = "completed"
			t.IsIntegrated = true
			t.UnintegratedCommits = 0
			t.ActionNeeded = fmt.Sprintf("No Action Required: Integrated into %s", parentState.BranchName)
			t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Integrated commits into %s (HEAD: %s)", parentState.BranchName, headDisplay))
			return nil
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "integrated",
			"task":   updated,
			"integration": map[string]any{
				"target_branch":         parentState.BranchName,
				"previous_target_head":  parentState.HeadCommit,
				"resulting_target_head": result.ResultingParentHead,
			},
		})
		return
	}

	// 7. Approve task session: POST /v3/projects/{id}/tasks/{taskId}/approve (or /accept)
	if len(segments) == 4 && segments[1] == "tasks" && (segments[3] == "approve" || segments[3] == "accept") {
		taskID := segments[2]
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		updated, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			t.Status = "in_progress"
			t.ActionNeeded = ""
			if len(t.WhatDidDo) == 0 {
				t.WhatDidDo = []string{"Mission approved by user", "Worktree session activated"}
			}
			return nil
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if updated != nil {
			proj, _, _ := db.GetProject(p.AccountScopeID, projectID)
			if updated.TaskProgram != nil || updated.TaskProgramID != "" {
				// Autonomous Task Program execution!
				_ = s.deployProjectTaskProgram(p, proj, updated)
			} else if updated.Agent == "image" || updated.Agent == "video" {
				// Direct media execution!
				_ = s.deployProjectTaskExecution(p, proj, updated, "in_progress", "")
				_ = db.PutProjectTask(p.AccountScopeID, updated)
			} else {
				// Agent session execution!
				if updated.SessionID == "" {
					if proj != nil {
						_ = s.deployProjectTaskExecution(p, proj, updated, "in_progress", "")
						_ = db.PutProjectTask(p.AccountScopeID, updated)
					}
				} else {
					// Session already created when task was proposed; now activate it with an approved run!
					now := time.Now().UnixMilli()
					runID := fmt.Sprintf("desktop-v3-run:%s", sessionruntime.NewSessionID())
					msgID := fmt.Sprintf("msg_%s_%d", updated.SessionID, now)
					msg := pebblestore.MessageSnapshot{
						ID:             msgID,
						SessionID:      updated.SessionID,
						UserID:         p.UserID,
						AccountScopeID: p.AccountScopeID,
						Role:           "user",
						Content:        "Task proposal approved. Proceed with execution.",
						CreatedAt:      now,
					}
					parentSessionID := ""
					if proj != nil {
						parentSessionID = proj.PrimarySessionID
					}
					runIntent := &pebblestore.V3SessionRunIntent{
						SessionID:       updated.SessionID,
						RunID:           runID,
						EpochID:         "epoch-00000000000000000001",
						UserID:          p.UserID,
						AccountScopeID:  p.AccountScopeID,
						ParentSessionID: parentSessionID,
						SourceMessageID: msgID,
						Status:          pebblestore.V3RunIntentPendingExecutor,
						CreatedAt:       now,
						UpdatedAt:       now,
					}
					approveKey := fmt.Sprintf("project-task:approve:%s:%d", updated.SessionID, now)
					_, _ = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
						SessionID:       updated.SessionID,
						UserID:          p.UserID,
						AccountScopeID:  p.AccountScopeID,
						ClientRequestID: approveKey,
						IdempotencyKey:  approveKey,
						PayloadHash:     approveKey,
						RequestHash:     approveKey,
						Kind:            pebblestore.V3SessionMutationAppendMessage,
						Message:         &msg,
						RunIntent:       runIntent,
						NowUnixMs:       now,
					})
					s.EnqueueSessionRun(p, updated.SessionID, runID, parentSessionID)
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "approved",
			"task":   updated,
		})
		return
	}

	// 7b. Reopen task session: POST /v3/projects/{id}/tasks/{taskId}/reopen
	if len(segments) == 4 && segments[1] == "tasks" && segments[3] == "reopen" {
		taskID := segments[2]
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		var req struct {
			Feedback string `json:"feedback,omitempty"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
		if len(body) > 0 {
			_ = json.Unmarshal(body, &req)
		}
		fb := strings.TrimSpace(req.Feedback)

		updated, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			t.Status = "in_progress"
			t.IsIntegrated = false
			t.ActionNeeded = "Task reopened by user"
			if fb != "" {
				t.FeedbackHistory = append(t.FeedbackHistory, fb)
				t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Reopened with instructions: %s", truncateString(fb, 50)))
			} else {
				t.WhatDidDo = append(t.WhatDidDo, "Task reopened for further execution")
			}
			return nil
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if updated != nil && updated.SessionID != "" {
			proj, _, _ := db.GetProject(p.AccountScopeID, projectID)
			now := time.Now().UnixMilli()
			runID := fmt.Sprintf("desktop-v3-run:%s", sessionruntime.NewSessionID())
			msgID := fmt.Sprintf("msg_%s_%d", updated.SessionID, now)
			promptMsg := "Task reopened by user. Please continue execution and complete all requirements."
			if fb != "" {
				promptMsg = fmt.Sprintf("Task reopened by user with feedback: %s\nPlease resume execution and address this.", fb)
			}
			msg := pebblestore.MessageSnapshot{
				ID:             msgID,
				SessionID:      updated.SessionID,
				UserID:         p.UserID,
				AccountScopeID: p.AccountScopeID,
				Role:           "user",
				Content:        promptMsg,
				CreatedAt:      now,
			}
			parentSessionID := ""
			if proj != nil {
				parentSessionID = proj.PrimarySessionID
			}
			runIntent := &pebblestore.V3SessionRunIntent{
				SessionID:       updated.SessionID,
				RunID:           runID,
				EpochID:         "epoch-00000000000000000001",
				UserID:          p.UserID,
				AccountScopeID:  p.AccountScopeID,
				ParentSessionID: parentSessionID,
				SourceMessageID: msgID,
				Status:          pebblestore.V3RunIntentPendingExecutor,
				CreatedAt:       now,
				UpdatedAt:       now,
			}
			reopenKey := fmt.Sprintf("project-task:reopen:%s:%d", updated.SessionID, now)
			_, _ = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
				SessionID:       updated.SessionID,
				UserID:          p.UserID,
				AccountScopeID:  p.AccountScopeID,
				ClientRequestID: reopenKey,
				IdempotencyKey:  reopenKey,
				PayloadHash:     reopenKey,
				RequestHash:     reopenKey,
				Kind:            pebblestore.V3SessionMutationAppendMessage,
				Message:         &msg,
				RunIntent:       runIntent,
				NowUnixMs:       now,
			})
			s.EnqueueSessionRun(p, updated.SessionID, runID, parentSessionID)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "reopened",
			"task":   updated,
		})
		return
	}

	// 7c. Complete task: POST /v3/projects/{id}/tasks/{taskId}/complete
	if len(segments) == 4 && segments[1] == "tasks" && segments[3] == "complete" {
		taskID := segments[2]
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		updated, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			t.Status = "completed"
			t.ActionNeeded = "Task marked completed by user"
			t.WhatDidDo = append(t.WhatDidDo, "Accepted and marked completed")
			return nil
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "completed",
			"task":   updated,
		})
		return
	}

	// 8. Refine task plan / error re-plan: POST /v3/projects/{id}/tasks/{taskId}/refine
	if len(segments) == 4 && segments[1] == "tasks" && segments[3] == "refine" {
		taskID := segments[2]
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
			return
		}
		type refineReq struct {
			Feedback     string `json:"feedback,omitempty"`
			ErrorSummary string `json:"error_summary,omitempty"`
		}
		var rReq refineReq
		if len(body) > 0 {
			_ = json.Unmarshal(body, &rReq)
		}

		proj, found, err := db.GetProject(p.AccountScopeID, projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || proj == nil {
			writeError(w, http.StatusNotFound, errors.New("project not found"))
			return
		}

		fb := strings.TrimSpace(rReq.Feedback)
		errSum := strings.TrimSpace(rReq.ErrorSummary)

		updated, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			if t.Revision <= 0 {
				t.Revision = 1
			}
			t.Revision++

			if fb != "" {
				t.FeedbackHistory = append(t.FeedbackHistory, fb)
			}
			if errSum != "" {
				t.LastError = errSum
			} else if fb != "" && t.LastError != "" {
				t.LastError = ""
			}

			seedPrompt := t.Description
			if seedPrompt == "" {
				seedPrompt = t.Title
			}
			taskRouter := taskrouter.NewService(func(ctx context.Context, instructions, input string) (string, error) {
				res, err := s.invokeConfiguredRouterOnce(ctx, p, instructions, input, 64<<10)
				if err != nil {
					return "", err
				}
				return res.Text, nil
			})
			routed := taskRouter.RefineTask(r.Context(), taskrouter.TaskRouteOptions{
				Prompt:             seedPrompt,
				RequestedWorkspace: t.WorkspacePath,
				Intent:             t.OutcomeType,
				AspectRatio:        t.AspectRatio,
				VariantCount:       t.VariantCount,
				ScenesCount:        len(t.Scenes),
				Soundtrack:         t.Soundtrack,
				Feedback:           fb,
				LastError:          t.LastError,
				Project:            proj,
			})

			t.Agent = routed.Agent
			t.OutcomeType = routed.OutcomeType
			t.Description = routed.Mission
			t.PipelineStages = routed.Stages
			t.Deliverables = routed.Deliverables
			t.WorkspacesInvolved = routed.WorkspacesInvolved
			t.ContextPoolSummary = routed.ContextPoolSummary
			t.PlanSummary = routed.PlanSummary
			t.FullPlanMarkdown = routed.FullPlanMarkdown
			t.Tier = routed.Tier
			t.AspectRatio = routed.AspectRatio
			t.VariantCount = routed.VariantCount
			t.Scenes = routed.Scenes
			t.Soundtrack = routed.Soundtrack
			t.RouterAlert = routed.RouterAlert
			t.Status = "pending_approval"
			t.ActionNeeded = fmt.Sprintf("Review revised plan (Rev %d) and click Approve", t.Revision)
			if fb != "" {
				t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Revised plan (Rev %d) based on: %s", t.Revision, truncateString(fb, 50)))
			} else if t.LastError != "" {
				t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Re-planned error recovery strategy (Rev %d)", t.Revision))
			}
			return nil
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if updated != nil && updated.SessionID != "" {
			refineMsg := fmt.Sprintf("## Plan Refined (Revision %d)\n\n**User Directives:** %s\n\n**Context Pool:** %s\n\n**Updated Plan:**\n%s", updated.Revision, fb, updated.ContextPoolSummary, updated.FullPlanMarkdown)
			_, _, _, _ = s.sessions.AppendMessage(updated.SessionID, "user", refineMsg, map[string]any{
				"role":                 "project_plan_refine",
				"revision":             updated.Revision,
				"context_pool_summary": updated.ContextPoolSummary,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "refined",
			"task":   updated,
		})
		return
	}

	// 9. Deploy task program: POST /v3/projects/{id}/tasks/{taskId}/program:deploy
	if len(segments) == 4 && segments[1] == "tasks" && (segments[3] == "program:deploy" || segments[3] == "deploy-program") {
		taskID := segments[2]
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || task == nil {
			writeError(w, http.StatusNotFound, errors.New("project task not found"))
			return
		}
		if task.TaskProgram == nil {
			writeError(w, http.StatusBadRequest, errors.New("task has no task program definition"))
			return
		}
		proj, _, _ := db.GetProject(p.AccountScopeID, projectID)
		if err := s.deployProjectTaskProgram(p, proj, task); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "deployed",
			"task":   task,
		})
		return
	}

	// 10. Redeploy task program job: POST /v3/projects/{id}/tasks/{taskId}/program:redeploy-job
	if len(segments) == 4 && segments[1] == "tasks" && (segments[3] == "program:redeploy-job" || segments[3] == "redeploy-job") {
		taskID := segments[2]
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		var req struct {
			JobID    string `json:"job_id"`
			Feedback string `json:"feedback,omitempty"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
		if len(body) > 0 {
			_ = json.Unmarshal(body, &req)
		}
		jobID := strings.TrimSpace(req.JobID)
		if jobID == "" {
			writeError(w, http.StatusBadRequest, errors.New("job_id is required"))
			return
		}
		if err := s.redeployTaskProgramJob(p, projectID, taskID, jobID, req.Feedback); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		updated, _, _ := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
		hydrateTaskProgramStatus(updated, db)
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "redeploying",
			"task":   updated,
		})
		return
	}

	// 11. Inspect task program record: GET /v3/projects/{id}/tasks/{taskId}/program
	if len(segments) == 4 && segments[1] == "tasks" && segments[3] == "program" {
		taskID := segments[2]
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
			return
		}
		task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || task == nil {
			writeError(w, http.StatusNotFound, errors.New("project task not found"))
			return
		}
		if task.TaskProgramID == "" || task.SessionID == "" {
			writeError(w, http.StatusNotFound, errors.New("task has no deployed task program"))
			return
		}
		prog, ok, err := db.GetTaskProgram(task.SessionID, task.TaskProgramID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("task program not found"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"program": prog,
		})
		return
	}

	writeError(w, http.StatusNotFound, errors.New("not found"))
}
