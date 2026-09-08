package run

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/taskscope"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: approval and inline parsing must agree before reservation or launch.
// Threat: an advertised alias is dropped or a later-stage filename glob reaches
// sparse allocation after earlier work has run. Exercise the real JSON decoder,
// executable-plan validator and task parser, the narrowest preflight boundaries.
func TestTaskProgramAuthoringContractPreflightParity(t *testing.T) {
	for _, tc := range []struct {
		name, scope string
		valid       bool
	}{
		{"exact", "src/item.go", true},
		{"directory", "src", true},
		{"subtree", "src/**", true},
		{"directory_alias", "./src/*", true},
		{"filename_glob", "src/item*.go", false},
		{"recursive_glob", "src/**/*.go", false},
		{"brace", "src/{a,b}.go", false},
		{"class", "src/[ab].go", false},
		{"question", "src/item?.go", false},
		{"traversal", "src/../escape", false},
		{"absolute", "/src/item.go", false},
		{"windows", `C:\src\item.go`, false},
		{"newline", "src/item.go\n/other", false},
		{"empty", "", false},
		{"whole", "**", false},
	} {
		for _, jobIndex := range []int{0, 2} {
			for _, identityKey := range []string{"agent_type", "subagent_type"} {
				t.Run(tc.name+string(rune('0'+jobIndex))+identityKey, func(t *testing.T) {
					args := taskProgramFixture(nil)
					program := args["program"].(map[string]any)
					jobs := program["jobs"].([]any)
					for _, raw := range jobs {
						job := raw.(map[string]any)
						if identityKey == "subagent_type" {
							job["subagent_type"] = job["agent_type"]
							delete(job, "agent_type")
						}
					}
					jobs[jobIndex].(map[string]any)["owned_scope"] = []any{tc.scope}
					raw, _ := json.Marshal(program)
					var definition pebblestore.TaskProgramDefinition
					if err := json.Unmarshal(raw, &definition); err != nil {
						t.Fatal(err)
					}
					doc := &pebblestore.SessionPlanDocument{
						Title: "Scope contract", Info: pebblestore.SessionPlanInfo{Goal: "Preflight every job"},
						Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Implement", Order: 1, Status: "pending", Tasks: []string{"Implement"}, AcceptanceCriteria: []string{"Preflight agrees"}, TaskProgram: &definition}},
					}
					before, _ := json.Marshal(doc)
					planErr := sessionruntime.ValidateExecutablePlanDocument(doc)
					parsed, parseErr := parseTaskCallArguments(mustJSON(t, args))
					if (planErr == nil) != tc.valid || (parseErr == nil) != tc.valid {
						t.Fatalf("valid=%v plan=%v parser=%v", tc.valid, planErr, parseErr)
					}
					after, _ := json.Marshal(doc)
					if string(before) != string(after) {
						t.Fatal("validation mutated plan")
					}
					if !tc.valid {
						if parsed.Program != nil || len(parsed.Launches) != 0 {
							t.Fatal("invalid program returned launchable partial state")
						}
						if !strings.Contains(planErr.Error(), "owned_scope") || !strings.Contains(parseErr.Error(), "owned_scope") {
							t.Fatalf("missing field diagnostic: %v / %v", planErr, parseErr)
						}
						return
					}
					canonical, _, err := taskProgramDefinitionFromSpec(parsed.Program)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(canonical, definition) {
						t.Fatalf("approval/start definitions differ: %#v / %#v", canonical, definition)
					}
					encoded, _ := json.Marshal(definition)
					if strings.Contains(string(encoded), "subagent_type") {
						t.Fatal("alias persisted as second identity")
					}
				})
			}
		}
	}
}

// Purpose: conflicting advertised identities must fail without replacing an
// existing job. TaskProgramJobSpec.UnmarshalJSON and inline parsing own this
// input boundary; no database or provider is needed to prove rejection.
func TestTaskProgramAuthoringContractAliasConflict(t *testing.T) {
	job := pebblestore.TaskProgramJobSpec{ID: "unchanged", AgentType: "finder"}
	before := job
	if err := json.Unmarshal([]byte(`{"agent_type":"coder","subagent_type":"finder"}`), &job); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("error=%v", err)
	}
	if !reflect.DeepEqual(job, before) {
		t.Fatal("failed decode changed job")
	}
	if err := json.Unmarshal([]byte(`{"agent_type":"coder","unexpected":true}`), &job); err == nil || !reflect.DeepEqual(job, before) {
		t.Fatalf("unknown field admitted or changed job: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"agent_type":"CODER","subagent_type":"coder"}`), &job); err != nil || job.AgentType != "coder" {
		t.Fatalf("matching alias rejected: %v", err)
	}
	args := taskProgramFixture(nil)
	args["program"].(map[string]any)["jobs"].([]any)[0].(map[string]any)["subagent_type"] = "finder"
	if parsed, err := parseTaskCallArguments(mustJSON(t, args)); err == nil || parsed.Program != nil {
		t.Fatalf("conflict admitted: %v", err)
	}
}

// Purpose: model guidance must advertise the scope grammar actually exercised
// above; this is a discoverability guard, not standalone security evidence.
func TestTaskProgramAuthoringContractGuidance(t *testing.T) {
	prompt := masterHarnessPrompt("/workspace")
	for _, required := range []string{taskscope.Guidance, "prefer jobs[].agent_type", "only a durable started/failed program requires a new ID"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("missing guidance %q", required)
		}
	}
	for _, definition := range tool.NewRuntime(1).Definitions() {
		if definition.Name != "task" {
			continue
		}
		properties := definition.Parameters["properties"].(map[string]any)
		program := properties["program"].(map[string]any)["properties"].(map[string]any)
		job := program["jobs"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
		if !strings.Contains(job["owned_scope"].(map[string]any)["description"].(string), taskscope.Guidance) {
			t.Fatal("schema guidance drift")
		}
		return
	}
	t.Fatal("task schema missing")
}

// Purpose: scope errors must not become artifact/permission failures merely
// because a filename contains those words. The scheduler owns recovery labels;
// errors.As must preserve the lexical preflight cause across wrapping.
func TestTaskProgramAuthoringContractScopeErrorClassification(t *testing.T) {
	for _, scope := range []string{"src/artifact*.go", "src/permission*.go", "src/stale*.go"} {
		_, _, err := taskscope.Canonical(scope)
		if err == nil {
			t.Fatal("invalid scope accepted")
		}
		if got := taskProgramErrorCode(fmt.Errorf("task failed to allocate subagent worktree: %w", err)); got != "planning_required" {
			t.Fatalf("scope %q classified %q", scope, got)
		}
	}
}

// Purpose: lexical validation must not allow directory aliases to hide overlap,
// or let workspace Designers use the Coder subtree grammar. Compare approval and
// inline start directly; no worker should be returned for either invalid graph.
func TestTaskProgramAuthoringContractOwnershipBoundaries(t *testing.T) {
	for _, variant := range []string{"overlap_alias", "designer_subtree", "designer_dot_prefix", "designer_exact"} {
		t.Run(variant, func(t *testing.T) {
			args := taskProgramFixture(nil)
			program := args["program"].(map[string]any)
			jobs := program["jobs"].([]any)
			first := jobs[0].(map[string]any)
			valid := variant == "designer_exact"
			if variant == "overlap_alias" {
				first["owned_scope"] = []any{"./web/src/*"}
			} else {
				program["stages"] = program["stages"].([]any)[:1]
				program["jobs"] = jobs[:1]
				first["agent_type"] = "designer"
				first["output_mode"] = "workspace"
				scope := "web/src/card.tsx"
				if variant == "designer_subtree" {
					scope = "web/src/**"
				}
				if variant == "designer_dot_prefix" {
					scope = "./web/src/card.tsx"
				}
				first["owned_scope"] = []any{scope}
			}
			var definition pebblestore.TaskProgramDefinition
			if err := json.Unmarshal([]byte(mustJSON(t, program)), &definition); err != nil {
				t.Fatal(err)
			}
			planErr := sessionruntime.ValidatePlanTaskProgramDefinition(&definition)
			parsed, parseErr := parseTaskCallArguments(mustJSON(t, args))
			if (planErr == nil) != valid || (parseErr == nil) != valid {
				t.Fatalf("plan=%v parser=%v", planErr, parseErr)
			}
			if !valid && (parsed.Program != nil || len(parsed.Launches) != 0) {
				t.Fatal("invalid ownership returned launches")
			}
		})
	}
}
