package run

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type ContextTokenReport struct {
	MasterHarnessChars    int
	MasterHarnessTokens   int
	AutoModeChars         int
	AutoModeTokens        int
	PlanModeChars         int
	PlanModeTokens        int
	SubagentPolicyChars   int
	SubagentPolicyTokens  int
	AgentProfileChars     int
	AgentProfileTokens    int
	TotalSystemInstChars  int
	TotalSystemInstTokens int
	TotalToolSchemaBytes  int
	TotalToolSchemaTokens int
	GrandTotalBytes       int
	GrandTotalTokens      int
}

func MeasureContextTokens() ContextTokenReport {
	scope := tool.WorkspaceScope{
		PrimaryPath: "/workspace/project",
		Roots:       []string{"/workspace/project"},
	}

	profile, _ := agentruntime.DefaultProfileByName("swarm")
	profile = pebblestore.NormalizeAgentProfile(profile)

	harness := masterHarnessPromptWithScope(scope)
	subagentPolicy := subagentPolicyInstructions(permission.SubagentPolicy{
		Mode:                          permission.SubagentModeBounded,
		AutomaticLaunchesPerParentRun: 5,
		ActiveChildLimit:              50,
		SwarmActiveChildLimit:         50,
		OverBudgetAction:              permission.SubagentOverBudgetAsk,
		RequireWriteIsolation:         true,
	})

	agentProfileBlock := strings.Join([]string{
		"Active agent profile:",
		"- name: " + profile.Name,
		"- mode: " + profile.Mode,
		"- runtime_contract: " + pebblestore.AgentProfileRuntimeMode(profile),
		profile.Prompt,
	}, "\n")

	modeAuto := modeCapabilityInstructions(sessionruntime.ModeAuto, true, profile)
	modePlan := modeCapabilityInstructions(sessionruntime.ModePlan, false, profile)

	var autoBlocks []string
	autoBlocks = append(autoBlocks, harness)
	autoBlocks = append(autoBlocks, subagentPolicy)
	autoBlocks = append(autoBlocks, agentProfileBlock)
	fullAutoBase := strings.Join(autoBlocks, "\n\n")
	fullAutoBase = appendHostRuntimeContext(fullAutoBase, scope.PrimaryPath, scope.Roots)
	fullAutoBase = appendWorktreeRuntimeContext(fullAutoBase, scope)
	fullAutoInstructions := composeModeAwareInstructions(fullAutoBase, sessionruntime.ModeAuto, true, profile)
	fullAutoWithModel := AppendResolvedModelPolicyInstructions(fullAutoInstructions, sessionruntime.ModeAuto, pebblestore.ModelPreference{
		Provider: "google",
		Model:    "gemini-3.8-flash",
		Thinking: "high",
	})

	rt := tool.NewRuntime(1)
	toolDefs := rt.Definitions()
	convertedTools := convertToolDefinitions(toolDefs)
	rawToolsJSON, _ := json.Marshal(convertedTools)

	report := ContextTokenReport{
		MasterHarnessChars:    len(harness),
		MasterHarnessTokens:   (len(harness) + 3) / 4,
		AutoModeChars:         len(modeAuto),
		AutoModeTokens:        (len(modeAuto) + 3) / 4,
		PlanModeChars:         len(modePlan),
		PlanModeTokens:        (len(modePlan) + 3) / 4,
		SubagentPolicyChars:   len(subagentPolicy),
		SubagentPolicyTokens:  (len(subagentPolicy) + 3) / 4,
		AgentProfileChars:     len(agentProfileBlock),
		AgentProfileTokens:    (len(agentProfileBlock) + 3) / 4,
		TotalSystemInstChars:  len(fullAutoWithModel),
		TotalSystemInstTokens: (len(fullAutoWithModel) + 3) / 4,
		TotalToolSchemaBytes:  len(rawToolsJSON),
		TotalToolSchemaTokens: (len(rawToolsJSON) + 3) / 4,
		GrandTotalBytes:       len(fullAutoWithModel) + len(rawToolsJSON),
		GrandTotalTokens:      (len(fullAutoWithModel) + len(rawToolsJSON) + 3) / 4,
	}
	return report
}

func TestContextTokenBenchmark(t *testing.T) {
	report := MeasureContextTokens()

	t.Logf("=== CONTEXT TOKEN BENCHMARK REPORT ===")
	t.Logf("Master Harness Prompt:    %6d chars (~%5d tokens)", report.MasterHarnessChars, report.MasterHarnessTokens)
	t.Logf("Subagent Policy:          %6d chars (~%5d tokens)", report.SubagentPolicyChars, report.SubagentPolicyTokens)
	t.Logf("Mode Capability (Auto):   %6d chars (~%5d tokens)", report.AutoModeChars, report.AutoModeTokens)
	t.Logf("Mode Capability (Plan):   %6d chars (~%5d tokens)", report.PlanModeChars, report.PlanModeTokens)
	t.Logf("Total System Instructions:%6d chars (~%5d tokens)", report.TotalSystemInstChars, report.TotalSystemInstTokens)
	t.Logf("Total Tool Schemas (40):  %6d bytes (~%5d tokens)", report.TotalToolSchemaBytes, report.TotalToolSchemaTokens)
	t.Logf("GRAND TOTAL (Turn 1):     %6d bytes (~%5d tokens)", report.GrandTotalBytes, report.GrandTotalTokens)
	t.Logf("=======================================")

	// Baseline ceilings before compression:
	// Master harness must not exceed 50,000 chars
	// Tool schemas must not exceed 75,000 bytes
	if report.MasterHarnessChars > 50000 {
		t.Fatalf("master harness prompt %d chars exceeds ceiling 50,000", report.MasterHarnessChars)
	}
	if report.TotalToolSchemaBytes > 75000 {
		t.Fatalf("tool schemas %d bytes exceeds ceiling 75,000", report.TotalToolSchemaBytes)
	}
}

func TestContextPerToolSchemaSizes(t *testing.T) {
	rt := tool.NewRuntime(1)
	toolDefs := rt.Definitions()
	converted := convertToolDefinitions(toolDefs)

	type toolInfo struct {
		name  string
		bytes int
	}
	var list []toolInfo
	for _, td := range converted {
		raw, _ := json.Marshal(td)
		list = append(list, toolInfo{name: td.Name, bytes: len(raw)})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].bytes > list[j].bytes
	})

	t.Logf("=== TOP 10 LARGEST TOOL SCHEMAS ===")
	for i := 0; i < 10 && i < len(list); i++ {
		t.Logf("%-25s %6d bytes (~%4d tokens)", list[i].name, list[i].bytes, list[i].bytes/4)
	}

	for _, td := range converted {
		if td.Name == "task" || td.Name == "plan_manage" || td.Name == "manage_artifact" || td.Name == "manage_video" {
			t.Logf("\n--- Property sizes for %s ---", td.Name)
			if props, ok := td.Parameters["properties"].(map[string]any); ok {
				type propSize struct {
					name  string
					bytes int
				}
				var pList []propSize
				for k, v := range props {
					b, _ := json.Marshal(v)
					pList = append(pList, propSize{name: k, bytes: len(b)})
				}
				sort.Slice(pList, func(i, j int) bool {
					return pList[i].bytes > pList[j].bytes
				})
				for _, p := range pList {
					t.Logf("   %-25s %5d bytes", p.name, p.bytes)
				}
			}
		}
	}
}
