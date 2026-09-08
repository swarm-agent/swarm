package run

import (
	"encoding/json"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
)

// Requirement: Coders author tests without shell access; the parent runs them.
// Regression: executable prompt examples must not assign impossible test runs or
// encourage permission expansion. Exercise emitted prompts and compiled policy,
// not source-file spelling. This is prompt/policy coverage, not provider proof.
func TestCoderParentValidationPromptContract(t *testing.T) {
	parent := masterHarnessPrompt(".")
	for _, text := range []string{
		"Coders do not get Bash or command execution by design",
		"the parent executes tests, builds, formatters",
		"not run; parent validation required",
		"batch independent tests in parallel",
		"never race tests against files another worker is editing",
		"a Task Program integration barrier is not an implicit parent test hook",
	} {
		if !strings.Contains(parent, text) {
			t.Errorf("parent prompt missing %q", text)
		}
	}
	coder := agentruntime.CoderAgentPrompt()
	for _, text := range []string{
		"You do not have Bash or command execution by design",
		"Do not treat missing Bash alone as a blocker",
		"not run; parent validation required",
		"Never claim tests passed without execution evidence",
		"parent returns test failures",
	} {
		if !strings.Contains(coder, text) {
			t.Errorf("Coder prompt missing %q", text)
		}
	}
	contract := agentruntime.CoderAgentToolContract()
	for _, name := range []string{"bash", "task"} {
		config, ok := contract.Tools[name]
		if !ok || config.Enabled == nil || *config.Enabled {
			t.Errorf("Coder must keep %s explicitly disabled", name)
		}
	}
	for _, name := range []string{"write", "edit", "git_add", "git_commit"} {
		config, ok := contract.Tools[name]
		if !ok || config.Enabled == nil || !*config.Enabled {
			t.Errorf("Coder must retain %s for authored handoffs", name)
		}
	}

	prefix := "- task (staged Task Program for a multi-subsystem build): "
	var example map[string]any
	for _, line := range strings.Split(parent, "\n") {
		if strings.HasPrefix(line, prefix) {
			// Decode exactly the JSON example; the explanatory suffix is prose.
			if err := json.NewDecoder(strings.NewReader(strings.TrimPrefix(line, prefix))).Decode(&example); err != nil {
				t.Fatalf("invalid staged example: %v", err)
			}
		}
	}
	if example == nil {
		t.Fatal("staged example missing")
	}
	program, ok := example["program"].(map[string]any)
	if !ok {
		t.Fatal("staged program missing")
	}
	jobs, ok := program["jobs"].([]any)
	if !ok || len(jobs) == 0 {
		t.Fatal("staged jobs missing")
	}
	for _, job := range jobs {
		body, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"run focused validation", "tests pass", "validated handoffs"} {
			if strings.Contains(string(body), forbidden) {
				t.Errorf("Coder example assigns execution or claims results: %s", body)
			}
		}
	}
}
