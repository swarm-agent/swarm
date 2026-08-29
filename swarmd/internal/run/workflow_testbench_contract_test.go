package run

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func readRepositoryScript(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	scriptPath := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "scripts", name))
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

func TestProviderBoundaryTestbenchCoversDelegatedWorktreesAcrossSecondBoundaries(t *testing.T) {
	script := readRepositoryScript(t, "v3-provider-boundary-e2e-testbench.sh")

	requiredGates := []string{
		"task_program_multi_worktree",
		"task_program_second_boundary",
		"individual_coder_worktree",
		"individual_coder_second_boundary",
	}
	for _, gate := range requiredGates {
		if !strings.Contains(script, `result.gates.`+gate+`=true`) || !strings.Contains(script, `'`+gate+`'`) {
			t.Errorf("provider-boundary testbench missing required gate %q", gate)
		}
	}

	for _, contract := range []string{
		"task_program:{id:",
		"max_concurrency:2",
		"task_program_id",
		"task_program_job_id",
		"new Set(taskProgramChildren.map(child=>child.worktree_root_path)).size===2",
		"individualChildren.length===1",
		"manage_worktree recall and integrate",
		"Task Program scheduler atomically integrates the stage",
		"manage_worktree promote",
		"transition_checkpoint_boundary",
		"assertCleanGitWorktree",
	} {
		if !strings.Contains(script, contract) {
			t.Errorf("provider-boundary testbench missing workflow contract %q", contract)
		}
	}

	if got := strings.Count(script, "SECOND_BOUNDARY_OK"); got < 2 {
		t.Errorf("provider-boundary testbench has %d delegated-workflow second-boundary sentinels, want at least 2", got)
	}
	if got := strings.Count(script, "session lane was not promoted into the captured checkout"); got != 2 {
		t.Errorf("provider-boundary testbench has %d promotion assertions, want 2", got)
	}
}

func TestTaskProgramWorktreePermissionRunnerCoversThreeCoderNestedIsolation(t *testing.T) {
	script := readRepositoryScript(t, filepath.Join("runners", "task-program-worktree-permissions.mjs"))

	for _, gate := range []string{
		"parent_managed_lane",
		"foundation_coders_isolated",
		"dependent_coder_saw_integrated_inputs",
		"three_committed_coder_handoffs",
		"parent_lane_integrated_clean",
		"source_dev_unchanged",
		"no_permission_requests",
		"bounded_step_evidence",
	} {
		if !strings.Contains(script, `'`+gate+`'`) || !strings.Contains(script, `result.gates.`+gate+` = true`) {
			t.Errorf("Task Program worktree runner missing required gate %q", gate)
		}
	}

	for _, contract := range []string{
		"require_write_isolation: true",
		"automatic_launches_per_parent_run: 5",
		"active_child_limit: 10",
		"worktree_mode: 'on'",
		"worktree_base_branch: 'dev'",
		"id: 'alpha'",
		"id: 'beta'",
		"id: 'verifier'",
		"depends_on: ['alpha', 'beta']",
		"Task Program advanced the captured source dev branch",
		"worktree_root_path !== repo",
		"worktree_root_path !== parent.worktree_root_path",
		"worktree_branch !== 'dev'",
		"worktree_clean === true",
		"status=all&limit=500",
		"permission.requested",
		"step 1: foundation Coder lanes allocated",
		"step 2: dependent Coder sees integrated prior outputs",
		"step 3: Task Program and checkpoint complete",
		"const heartbeatEveryMs = 15000",
		"const stallTimeoutMs = Math.min",
		"stalled for ${stallTimeoutMs}ms without durable state progress",
		"reached terminal state without required evidence",
	} {
		if !strings.Contains(script, contract) {
			t.Errorf("Task Program worktree runner missing contract %q", contract)
		}
	}
}

func TestWorkspaceMapCheckpointRunnerCoversRefreshAndGuardedSelfUpdate(t *testing.T) {
	script := readRepositoryScript(t, filepath.Join("runners", "workspace-map-checkpoints.mjs"))

	for _, gate := range []string{
		"explicit_update_applied",
		"same_execution_later_checkpoint",
		"fresh_provider_run_visibility",
		"self_update_applied",
		"revision_and_digest_changed",
		"durable_session_evidence",
	} {
		if !strings.Contains(script, `'`+gate+`'`) || !strings.Contains(script, `result.gates.`+gate+` = true`) {
			t.Errorf("Workspace Map runner missing required gate %q", gate)
		}
	}

	for _, contract := range []string{
		"manage_workspace inspect_map",
		"manage_workspace update_map",
		"expected_revision",
		"cp-map-update and cp-map-observe",
		"exit_plan_mode",
		"new Set(['exit_plan_mode', 'workspace_map_update'])",
		"payload.approved_arguments || decodeObject(permission.approved_arguments)",
		"approvedArguments?.permission_scope === 'workspace_map_update'",
		"...(approvedArguments ? { approved_arguments: approvedArguments } : {})",
		"SAME_EXECUTION_MARKER",
		"FRESH_RUN_MARKER",
		"SELF_UPDATE_MARKER",
		"stageTimeoutMs = Math.min(timeoutMs, 9 * 60 * 1000)",
		"const heartbeatEveryMs = 15000",
		"const terminalEvidenceGraceMs = 5000",
		"nextHeartbeat = Date.now() + heartbeatEveryMs",
		"reached terminal runs without required evidence",
		"step 1: explicit map update",
		"step 2: later checkpoint observation",
		"step 3: fresh-run injected marker visibility",
		"step 4: requested self-update",
		"step 5: self-update terminal confirmation",
		"history: { mode: 'full'",
		"checkpointIntents.length === 2",
		"afterSecond.revision",
		"afterSecond.digest !== afterFirst.digest",
	} {
		if !strings.Contains(script, contract) {
			t.Errorf("Workspace Map runner missing contract %q", contract)
		}
	}
}
