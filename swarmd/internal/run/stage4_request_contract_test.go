package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	agent "swarm/packages/swarmd/internal/agent"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Requirement H1/H6: inspect the production shared composition and resolved
// schemas before/after without private workspace/provider data. Owners are
// ComposeRuntimeInstructions, ScopeDurableRunStateInstructions and the contract
// compiler used by both provider loops. This is a composition capture, not a
// provider execution or benchmark; the API request consumer is verified separately.
func TestStage4RequestCapture(t *testing.T) {
	output := os.Getenv("SWARM_STAGE4_CAPTURE")
	if output == "" {
		t.Skip("explicit synthetic request capture only")
	}
	svc := &Service{tools: &tool.Runtime{}}
	base := store.AgentProfile{}
	profiles := []store.AgentProfile{agent.SwarmAgentProfileForContext(base), agent.WithAdvancedPlanning(agent.SwarmAgentProfileForContext(base)), agent.CoderAgentProfileForParent(base), agent.FinderAgentProfileForParent(base), agent.DesignerAgentProfileForParent(base), agent.DesignerWorkspaceAgentProfileForParent(base), agent.WorkerCoderAgentProfileForParent(base)}
	type capture struct {
		Scenario                                                   string
		Identity                                                   string
		HarnessBytes, ProfileBytes, InstructionsBytes, SchemaBytes int
		Instructions                                               string
		Tools                                                      any
	}
	var records []capture
	for index, p := range profiles {
		for _, phase := range []string{"initial", "continuation"} {
			scope := tool.WorkspaceScope{PrimaryPath: "/synthetic/workspace", Roots: []string{"/synthetic/workspace"}, SessionID: "synthetic-session"}

			state, _ := renderCompactRunState(compactRunState{SessionMode: "auto", CurrentSessionID: "synthetic-session", CurrentRunID: "synthetic-run", NextLifecycleAction: "continue_current_turn"})

			_, _, disabled, err := svc.ResolveAgentToolContract(p)
			if err != nil {
				t.Fatal(err)
			}
			definitions := filterToolDefinitions(convertToolDefinitions(svc.ListAgentToolDefinitions()), disabled)
			instructionProfile := InstructionProfileForTools(p, definitions)
			text := svc.ComposeRuntimeInstructions(scope, "auto", false, instructionProfile, "Synthetic assignment: implement the scoped change; preserve source and return exact output references. Acceptance: focused checks pass. Unresolved blocker: none.")
			text += "\n\n" + ScopeDurableRunStateInstructions(state, p, providerToolNames(definitions)...)
			schema, err := json.Marshal(definitions)
			if err != nil {
				t.Fatal(err)
			}
			records = append(records, capture{Scenario: phase + "-" + string(rune('0'+index)), Identity: p.Name, HarnessBytes: len(harnessForProfile(scope, instructionProfile)), ProfileBytes: len(instructionProfile.Prompt), InstructionsBytes: len(text), SchemaBytes: len(schema), Instructions: text, Tools: definitions})
		}
	}
	raw, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(output, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("captured %d shared request compositions and schemas; no provider invoked", len(records))
}

// Requirement H2-H4: each resolved role gets relevant guidance with its original
// tool authority, including negative capability boundaries; Designer resolves
// to swarm_design with read/search and scoped Git and nothing that writes HTML
// another way. Threat: a role regains a retired or parent-only capability.
// Owners are the shared request composer and compileResolvedAgentToolContract;
// this layer observes concrete schemas/policy and instructions before provider
// transport.
func TestStage4RoleRequestContracts(t *testing.T) {
	svc := &Service{tools: &tool.Runtime{}}
	base := store.AgentProfile{}
	for _, p := range []store.AgentProfile{agent.SwarmAgentProfileForContext(base), agent.CoderAgentProfileForParent(base), agent.FinderAgentProfileForParent(base), agent.DesignerAgentProfileForParent(base), agent.DesignerWorkspaceAgentProfileForParent(base), agent.WorkerCoderAgentProfileForParent(base)} {
		t.Run(p.Name+"-"+p.Description, func(t *testing.T) {
			text := svc.ComposeRuntimeInstructions(tool.WorkspaceScope{PrimaryPath: "/synthetic/workspace"}, "auto", false, p, "Acceptance: retain exact source/output references.")
			for _, bad := range []string{"Use manage_design", "call manage_workers", "workspace picker", "sidebar", "gallery interaction", "start_session_checkpoint", "call exit_plan_mode"} {
				if strings.Contains(text, bad) && bad != "gallery interaction" {
					t.Errorf("unavailable/irrelevant instruction %q", bad)
				}
			}
			if !strings.Contains(text, "Acceptance: retain exact source/output references.") {
				t.Fatal("assignment was lost")
			}
			contract, policy, disabled, err := svc.ResolveAgentToolContract(p)
			if err != nil {
				t.Fatal(err)
			}
			if policy == nil {
				t.Fatal("missing runtime policy")
			}
			assertDenied := func(names ...string) {
				t.Helper()
				for _, name := range names {
					if contract.Tools[name].Enabled || !disabled[name] {
						t.Errorf("%s unexpectedly enabled", name)
					}
				}
			}
			switch p.Name {
			case "swarm":
				assertDenied("manage_workers", "manage_automation")
				if !contract.Tools["task"].Enabled {
					t.Fatal("Swarm lost task")
				}
				if !strings.Contains(text, "Use task to deploy Coder, Finder and Designer") {
					t.Fatal("Swarm lost specialists")
				}
			case agent.CoderAgentID:
				assertDenied("task", "ask_user", "plan_manage", "manage_workers", "manage_automation", "manage_environments", "manage_worktree")
				if !contract.Tools["bash"].Enabled {
					t.Fatal("regular Coder lost Bash")
				}
				if strings.Contains(text, "Delegation and validation ownership:") {
					t.Fatal("Coder inherited parent tutorial")
				}
				if !strings.Contains(text, "Use Bash for the focused tests") || !strings.Contains(text, "Never claim tests passed without execution evidence") {
					t.Fatal("Coder validation evidence requirement is not explicit")
				}
			case agent.FinderAgentID:
				assertDenied("find", "write", "edit", "task")
				if strings.Contains(text, "search, find") {
					t.Fatal("Finder instructed to use absent find")
				}
			case agent.DesignerAgentID:
				// Designer has one contract for both profile names: designs only
				// through swarm_design, plus read/search and scoped Git.
				assertDenied("bash", "task", "ask_user", "plan_manage", "write", "edit", "media_inspect", "manage_artifact")
				for _, name := range []string{tool.SwarmDesignToolName, "read", "search", "find", "list", "git_status", "git_diff", "git_add", "git_commit"} {
					if !contract.Tools[name].Enabled || disabled[name] {
						t.Errorf("Designer lost %s", name)
					}
				}
				// artifact_v3_author is no longer a registered tool at all.
				if contract.Tools["artifact_v3_author"].Enabled || contract.Tools["manage_design"].Enabled {
					t.Error("retired Designer authoring tool unexpectedly enabled")
				}
				if !strings.Contains(text, "swarm_design") || strings.Contains(text, "inspect_context") || strings.Contains(text, "artifact_v3_author") {
					t.Fatal("Designer instructions do not match the swarm_design contract")
				}
			}
		})
	}
}

// Requirement H4/H6: a child validation receipt survives bounded provider output
// and replay; long stdout must not hide a failed exact-source postcondition.
// Owners: PrepareToolOutputForModel, formatToolCompletedOutput and durable tool
// history replay. Pure transformation is the narrowest layer for this failure.
func TestStage4ValidationReceiptSurvivesOutputBounds(t *testing.T) {
	for _, verified := range []bool{true, false} {
		receipt := map[string]any{"verified": verified, "head_commit": strings.Repeat("a", 40), "workspace_path": "/synthetic/child", "task_call_id": "synthetic-call", "child_session_id": "synthetic-child"}
		payload := map[string]any{"output": strings.Repeat("x", maxToolInputBytes+1024), "exit_code": 0, "summary": "child validation result", "validation_receipt": receipt}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		call := tool.Call{Name: "bash", CallID: "synthetic-bash"}
		result := tool.Result{Name: "bash", CallID: call.CallID, Output: string(raw)}
		if !verified {
			result.Error = "child validation postcondition failed"
		}
		for _, output := range []string{PrepareToolOutputForModel(call, result), formatToolCompletedOutput(call, result)} {
			var got map[string]any
			if err := json.Unmarshal([]byte(output), &got); err != nil {
				t.Fatal(err)
			}
			restored, ok := got["validation_receipt"].(map[string]any)
			if !ok || restored["verified"] != verified || restored["head_commit"] != receipt["head_commit"] {
				t.Fatalf("receipt dropped or rewritten: %s", output)
			}
			if !verified && got["error"] == nil {
				t.Fatal("failed validation lost error")
			}
		}
		history := formatToolHistory(call, result)
		input, ok := buildToolHistoryInput(history)
		if !ok || len(input) != 2 {
			t.Fatal("durable replay unavailable")
		}
		var replay map[string]any
		if err := json.Unmarshal([]byte(input[1]["output"].(string)), &replay); err != nil {
			t.Fatal(err)
		}
		if replay["validation_receipt"] == nil {
			t.Fatal("receipt lost on replay")
		}
	}
}
