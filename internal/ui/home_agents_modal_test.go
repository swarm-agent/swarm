package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

func canonicalAgentsModalTestData() AgentsModalData {
	assignment := func(model string) client.AgentModelAssignment {
		return client.AgentModelAssignment{Provider: "codex", Model: model, Thinking: "high", ServiceTier: "priority"}
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
	if got, want := canonicalAgentModelNames, []string{"swarm", "finder", "coder", "designer", "compact", "router"}; len(got) != len(want) {
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
	if got := page.selectedAgentsModalAssignments(); len(got) != 1 || got[0].Model != "finder-model" {
		t.Fatalf("Finder assignments = %#v", got)
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

func TestAgentsModalAgentCardLinesUseCanonicalAssignment(t *testing.T) {
	assignment := client.AgentModelAssignment{Provider: "codex", Model: "gpt-5.6", Thinking: "xhigh", ServiceTier: "priority"}
	if got := agentsModalAssignmentModelLine(assignment); got != "codex/gpt-5.6" {
		t.Fatalf("model line = %q, want codex/gpt-5.6", got)
	}
	if got := agentsModalAssignmentSettingsLine(assignment); got != "xhigh • priority" {
		t.Fatalf("settings line = %q", got)
	}
}

func TestAgentsModalAgentCardUsesOneOutlineAndPanelBackground(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(canonicalAgentsModalTestData())
	page.theme.Panel = tcell.StyleDefault.Background(tcell.ColorBlack)
	page.theme.Element = tcell.StyleDefault.Background(tcell.ColorGreen)
	page.theme.Border = tcell.StyleDefault.Foreground(tcell.ColorGray).Background(tcell.ColorBlack)
	page.theme.BorderActive = tcell.StyleDefault.Foreground(tcell.ColorBlue).Background(tcell.ColorBlack)
	page.theme.Text = tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorRed)
	page.theme.TextMuted = tcell.StyleDefault.Foreground(tcell.ColorGray).Background(tcell.ColorPurple)
	page.theme.Accent = tcell.StyleDefault.Foreground(tcell.ColorYellow).Background(tcell.ColorMaroon)

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(110, 36)
	page.drawAgentsModal(screen)
	screen.Show()

	for _, point := range []struct {
		x int
		y int
	}{
		{x: 7, y: 7},  // model text
		{x: 7, y: 8},  // settings text
		{x: 40, y: 7}, // card padding
	} {
		_, _, style, _ := screen.GetContent(point.x, point.y)
		_, background, _ := style.Decompose()
		if background != tcell.ColorBlack {
			t.Fatalf("card cell (%d,%d) background = %v, want panel background", point.x, point.y, background)
		}
	}
	for _, point := range []struct {
		x    int
		y    int
		want rune
	}{
		{x: 4, y: 6, want: tcell.RuneULCorner},
		{x: 42, y: 6, want: tcell.RuneURCorner},
		{x: 4, y: 9, want: tcell.RuneLLCorner},
		{x: 42, y: 9, want: tcell.RuneLRCorner},
	} {
		got, _, _, _ := screen.GetContent(point.x, point.y)
		if got != point.want {
			t.Fatalf("card outline cell (%d,%d) = %q, want %q", point.x, point.y, got, point.want)
		}
	}
}

func TestAgentsModalRenderSeparatesCoreAgentsFromUtilities(t *testing.T) {
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
	core, finder, coder, designer := strings.Index(text, "Core system agents"), strings.Index(text, "Finder"), strings.Index(text, "Coder"), strings.Index(text, "Designer")
	utilities, compact, router := strings.Index(text, "Utilities"), strings.Index(text, "Compact"), strings.Index(text, "Router")
	if !(core >= 0 && core < finder && finder < coder && coder < designer && designer < utilities && utilities < compact && compact < router) {
		t.Fatalf("agent groups are not rendered in canonical order:\n%s", text)
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
	for _, want := range []string{"Swarm", "codex/action-model", "high • priority", "Compact", "Finder", "Coder", "Designer", "Router", "Default model", "Plan model", "Save & Continue", "Save & Exit"} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q:\n%s", want, text)
		}
	}
	for _, rejected := range []string{"thinking:", "priority:", "Profile", "single", "split", "temporary", "Save as new"} {
		if strings.Contains(text, rejected) {
			t.Fatalf("render retained %q workflow:\n%s", rejected, text)
		}
	}
}

func testManyModelsAgentsData(modelCount int) AgentsModalData {
	models := make([]string, modelCount)
	catalog := make(map[string]client.ModelCatalogRecord, modelCount)
	for i := 0; i < modelCount; i++ {
		name := fmt.Sprintf("model-%02d", i+1)
		models[i] = name
		catalog["codex/"+name] = client.ModelCatalogRecord{
			Provider:        "codex",
			Model:           name,
			ThinkingOptions: []string{"off", "high"},
		}
	}
	assignment := client.AgentModelAssignment{
		Provider: "codex",
		Model:    models[0],
		Thinking: "high",
	}
	return AgentsModalData{
		Settings: client.AgentModelSettings{
			Swarm: client.SwarmAgentModelAssignments{
				Action: assignment,
				Plan:   assignment,
			},
			SystemAgents: client.SystemAgentModelAssignments{
				Compact:  assignment,
				Finder:   assignment,
				Coder:    assignment,
				Designer: assignment,
				Router:   assignment,
			},
		},
		Providers:        []string{"codex"},
		ModelsByProvider: map[string][]string{"codex": models},
		ModelCatalog:     catalog,
	}
}

func renderAgentsModalScreen(page *HomePage, w, h int) string {
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		panic(err)
	}
	defer screen.Fini()
	screen.SetSize(w, h)
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
	return rendered.String()
}

func TestAgentsModalModelListScrollsDownToBottomWhenManyModels(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(testManyModelsAgentsData(40))

	// Navigate to Fields -> Model field
	page.agentsModal.Focus = agentsModalFocusFields
	page.agentsModal.SelectedField = 1 // Model field

	// Press Enter to start editing the Model field
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if !page.agentsModal.EditingField {
		t.Fatal("expected EditingField to be true after Enter on Model field")
	}
	if got := page.agentsModal.EditingOption; got != "model-01" {
		t.Fatalf("initial editing option = %q, want model-01", got)
	}

	// Render on a 30-row screen
	text := renderAgentsModalScreen(page, 100, 30)
	if !strings.Contains(text, "model-01") {
		t.Fatalf("expected model-01 visible initially:\n%s", text)
	}
	if strings.Contains(text, "model-40") {
		t.Fatalf("model-40 should not be visible initially before scrolling:\n%s", text)
	}

	// Press Down repeatedly to scroll down to the bottom
	for i := 0; i < 50; i++ {
		page.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	}

	// Must be clamped at the bottom model (model-40)
	if got := page.agentsModal.EditingOption; got != "model-40" {
		t.Fatalf("after pressing Down to bottom, got %q, want model-40", got)
	}

	// Render at bottom: model-40 must be visible and selected with "> model-40"
	textAtBottom := renderAgentsModalScreen(page, 100, 30)
	if !strings.Contains(textAtBottom, "model-40") {
		t.Fatalf("expected model-40 to be visible when scrolled to bottom:\n%s", textAtBottom)
	}
	if !strings.Contains(textAtBottom, "> model-40") {
		t.Fatalf("expected model-40 to be selected with '> model-40':\n%s", textAtBottom)
	}
	if strings.Contains(textAtBottom, "    model-01") || strings.Contains(textAtBottom, "  > model-01") {
		t.Fatalf("model-01 should not be visible in options list when scrolled all the way to bottom:\n%s", textAtBottom)
	}

	// Press Down again: must stay at bottom and keep model-40 visible
	page.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if got := page.agentsModal.EditingOption; got != "model-40" {
		t.Fatalf("pressing Down at bottom should stay at model-40, got %q", got)
	}
	textStillAtBottom := renderAgentsModalScreen(page, 100, 30)
	if !strings.Contains(textStillAtBottom, "> model-40") {
		t.Fatalf("expected model-40 still visible and selected:\n%s", textStillAtBottom)
	}

	// Press Up: moves to model-39
	page.HandleKey(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))
	if got := page.agentsModal.EditingOption; got != "model-39" {
		t.Fatalf("after Up, got %q, want model-39", got)
	}

	// Press Home: jumps to top (model-01)
	page.HandleKey(tcell.NewEventKey(tcell.KeyHome, 0, tcell.ModNone))
	if got := page.agentsModal.EditingOption; got != "model-01" {
		t.Fatalf("after Home, got %q, want model-01", got)
	}
	textAtTop := renderAgentsModalScreen(page, 100, 30)
	if !strings.Contains(textAtTop, "> model-01") {
		t.Fatalf("expected model-01 visible and selected after Home:\n%s", textAtTop)
	}

	// Press End: jumps back to bottom (model-40)
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnd, 0, tcell.ModNone))
	if got := page.agentsModal.EditingOption; got != "model-40" {
		t.Fatalf("after End, got %q, want model-40", got)
	}
	textAtEnd := renderAgentsModalScreen(page, 100, 30)
	if !strings.Contains(textAtEnd, "> model-40") {
		t.Fatalf("expected model-40 visible and selected after End:\n%s", textAtEnd)
	}

	// Press Enter to commit selection
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if page.agentsModal.EditingField {
		t.Fatal("expected EditingField to be false after Enter")
	}
	assignment := page.selectedAgentsModalAssignment()
	if assignment.Model != "model-40" {
		t.Fatalf("committed model = %q, want model-40", assignment.Model)
	}
}

func TestAgentsModalModelListOpensWithCurrentModelScrolledIntoView(t *testing.T) {
	data := testManyModelsAgentsData(40)
	data.Settings.Swarm.Action.Model = "model-35" // Model near bottom

	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(data)

	page.agentsModal.Focus = agentsModalFocusFields
	page.agentsModal.SelectedField = 1 // Model field

	// Open field edit
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if got := page.agentsModal.EditingOption; got != "model-35" {
		t.Fatalf("editing option = %q, want model-35", got)
	}

	// Render: model-35 must be scrolled into view immediately
	text := renderAgentsModalScreen(page, 100, 30)
	if !strings.Contains(text, "> model-35") {
		t.Fatalf("expected model-35 to be visible and selected upon opening edit:\n%s", text)
	}
}

func TestAgentsModalModelListMouseWheelScroll(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAgentsModal()
	page.SetAgentsModalData(testManyModelsAgentsData(40))

	page.agentsModal.Focus = agentsModalFocusFields
	page.agentsModal.SelectedField = 1
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	// Initial option: model-01
	if got := page.agentsModal.EditingOption; got != "model-01" {
		t.Fatalf("initial option = %q, want model-01", got)
	}

	// Wheel down: advances by 3
	page.HandleMouse(tcell.NewEventMouse(50, 15, tcell.WheelDown, 0))
	if got := page.agentsModal.EditingOption; got != "model-04" {
		t.Fatalf("after WheelDown, option = %q, want model-04", got)
	}

	// Wheel down again
	page.HandleMouse(tcell.NewEventMouse(50, 15, tcell.WheelDown, 0))
	if got := page.agentsModal.EditingOption; got != "model-07" {
		t.Fatalf("after second WheelDown, option = %q, want model-07", got)
	}

	// Wheel up: scrolls back by 3
	page.HandleMouse(tcell.NewEventMouse(50, 15, tcell.WheelUp, 0))
	if got := page.agentsModal.EditingOption; got != "model-04" {
		t.Fatalf("after WheelUp, option = %q, want model-04", got)
	}
}
