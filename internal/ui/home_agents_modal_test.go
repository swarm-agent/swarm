package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

func canonicalAgentsModalTestData() AgentsModalData {
	assignment := func(model string) client.AgentModelAssignment {
		return client.AgentModelAssignment{Provider: "codex", Model: model, Thinking: "high"}
	}
	return AgentsModalData{
		Settings: client.AgentModelSettings{
			Swarm: client.SwarmAgentModelAssignments{Action: assignment("action-model"), Plan: assignment("plan-model")},
			SystemAgents: client.SystemAgentModelAssignments{
				Compact: assignment("compact-model"), Finder: assignment("finder-model"), Coder: assignment("coder-model"),
				Designer: assignment("designer-model"), Router: assignment("router-model"),
			},
		},
		Providers:        []string{"codex", "anthropic"},
		ModelsByProvider: map[string][]string{"codex": {"action-model", "plan-model", "next-model"}, "anthropic": {"claude"}},
		ModelCatalog: map[string]client.ModelCatalogRecord{
			"codex/action-model": {Provider: "codex", Model: "action-model", ThinkingOptions: []string{"low", "high"}, ServiceTiers: []string{"standard", "priority"}, ServiceTierMappings: []client.ModelCatalogServiceTierMapping{{Tier: "priority", SwarmSetting: "fast"}}},
			"codex/plan-model":   {Provider: "codex", Model: "plan-model", ThinkingOptions: []string{"high", "xhigh"}, ServiceTiers: []string{"fast"}},
			"codex/next-model":   {Provider: "codex", Model: "next-model", ThinkingOptions: []string{"high"}, ServiceTiers: []string{"fast"}},
			"anthropic/claude":   {Provider: "anthropic", Model: "claude", ThinkingOptions: []string{"high"}},
		},
	}
}

func TestAgentsModalPriorityUsesCanonicalSwarmValues(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(canonicalAgentsModalTestData())
	page.agentsModal.Focus = agentsModalFocusFields
	page.agentsModal.SelectedField = 3
	if got, want := page.agentsModalSelectedFieldOptions(), []string{"", "priority", "fast"}; len(got) != len(want) {
		t.Fatalf("priority options = %#v, want %#v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("priority options = %#v, want %#v", got, want)
			}
		}
	}
}

func TestAgentsModalCanonicalAgentListStartsOnSwarm(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(canonicalAgentsModalTestData())

	if got := page.selectedAgentsModalName(); got != "swarm" {
		t.Fatalf("selected agent = %q, want swarm", got)
	}
	if got, want := canonicalAgentModelNames, []string{"swarm", "compact", "finder", "coder", "designer", "router"}; len(got) != len(want) {
		t.Fatalf("agents = %#v, want %#v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("agent[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	}
}

func TestAgentsModalSwarmHasDefaultThenPlanAndSystemAgentHasOneAssignment(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(canonicalAgentsModalTestData())

	if got := page.selectedAgentsModalAssignments(); len(got) != 2 || got[0].Model != "action-model" || got[1].Model != "plan-model" {
		t.Fatalf("Swarm assignments = %#v", got)
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if page.agentsModal.Focus != agentsModalFocusAssignments || page.agentsModal.SelectedAssignment != 0 {
		t.Fatalf("Enter focus = %v assignment %d", page.agentsModal.Focus, page.agentsModal.SelectedAssignment)
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if page.agentsModal.SelectedAssignment != 1 {
		t.Fatalf("Down did not select Plan model")
	}

	page.agentsModal.Focus = agentsModalFocusAgents
	page.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if got := page.selectedAgentsModalAssignments(); len(got) != 1 || got[0].Model != "compact-model" {
		t.Fatalf("Compact assignments = %#v", got)
	}
}

func TestAgentsModalCtrlYSavesCompleteCanonicalPatchAndKeepsModalOpen(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(canonicalAgentsModalTestData())
	page.agentsModal.Drafts["swarm"][0].Model = "next-model"

	page.HandleKey(tcell.NewEventKey(tcell.KeyCtrlY, 0, tcell.ModCtrl))
	action, ok := page.PopAgentsModalAction()
	if !ok || action.Kind != AgentsModalActionSave || action.Swarm == nil {
		t.Fatalf("save action = %#v", action)
	}
	if action.Swarm.Action.Model != "next-model" || action.Swarm.Plan.Model != "plan-model" {
		t.Fatalf("Swarm patch = %#v", action.Swarm)
	}
	if !page.AgentsModalVisible() || !page.agentsModal.Loading {
		t.Fatal("modal closed before App confirmed API success")
	}
}

func TestAgentsModalFinalSaveControlQueuesSystemAgentPatch(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(canonicalAgentsModalTestData())
	page.agentsModal.SelectedAgent = 5
	page.agentsModal.Drafts["router"][0].Thinking = "xhigh"
	page.agentsModal.Focus = agentsModalFocusSave

	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	action, ok := page.PopAgentsModalAction()
	if !ok || action.Agent != "router" || action.Assignment == nil || action.Assignment.Thinking != "xhigh" {
		t.Fatalf("Router save action = %#v", action)
	}
}

func TestAgentsModalSaveLoadingCannotBeCancelledBeforeAppResponse(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(canonicalAgentsModalTestData())
	page.agentsModal.Drafts["swarm"][0].Thinking = "low"
	page.HandleKey(tcell.NewEventKey(tcell.KeyCtrlY, 0, tcell.ModCtrl))
	page.HandleKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if !page.AgentsModalVisible() || !page.agentsModal.Loading {
		t.Fatal("save-in-flight modal closed before App response")
	}
}

func TestAgentsModalEscCancelsWithoutPersisting(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(canonicalAgentsModalTestData())
	page.agentsModal.Drafts["swarm"][0].Thinking = "low"
	page.HandleKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if page.AgentsModalVisible() {
		t.Fatal("Esc left canonical Agents modal open")
	}
	if _, ok := page.PopAgentsModalAction(); ok {
		t.Fatal("Esc queued persistence")
	}
}

func TestAgentsModalRenderHasNoProfileOrPolicyWorkflow(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(canonicalAgentsModalTestData())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(110, 36)
	page.drawAgentsModal(screen)
	screen.Show()
	cells, width, _ := screen.GetContents()
	var rendered strings.Builder
	for i, cell := range cells {
		if i > 0 && i%width == 0 {
			rendered.WriteByte('\n')
		}
		if len(cell.Runes) > 0 {
			rendered.WriteRune(cell.Runes[0])
		} else {
			rendered.WriteByte(' ')
		}
	}
	text := rendered.String()
	for _, want := range []string{"Swarm", "Compact", "Finder", "Coder", "Designer", "Router", "Default model", "Plan model", "Save changes and exit"} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q:\n%s", want, text)
		}
	}
	for _, rejected := range []string{"Profile", "single", "split", "temporary", "Save as new"} {
		if strings.Contains(text, rejected) {
			t.Fatalf("render retained %q workflow:\n%s", rejected, text)
		}
	}
}
