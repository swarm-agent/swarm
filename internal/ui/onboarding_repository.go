package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"swarm-refactor/swarmtui/internal/client"
)

type onboardingControl struct{ label, action, path string }

func hasAgentsMD(dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "AGENTS.md"))
	return err == nil && !info.IsDir()
}

func (p *HomePage) repositoryControls() []onboardingControl {
	s := &p.onboarding
	if s.PreFinish {
		projName := strings.TrimSpace(s.PreFinishProjectName)
		if projName == "" {
			projName = strings.TrimSpace(s.ProjectName)
		}
		if projName == "" {
			projName = "Project"
		}
		return []onboardingControl{
			{label: fmt.Sprintf("[ Launch %s & Talk to Swarm → ]", projName), action: "finish_prefinish"},
		}
	}
	if s.SetupConsent {
		return []onboardingControl{{"Create folder + Git first commit + open workspace", "setup", ""}, {"Cancel", "cancel", ""}}
	}
	if s.Review != nil {
		controls := []onboardingControl{}
		if s.Review.Repository.HeadCommit == "" {
			for _, f := range s.Review.Files {
				mark := "[ ]"
				if s.Selected[f.Path] {
					mark = "[x]"
				}
				if !f.Selectable {
					mark = "[-]"
				}
				controls = append(controls, onboardingControl{fmt.Sprintf("%s %s (%d bytes)", mark, f.Path, f.Size), "file", f.Path})
			}
		}
		mark := "[ ]"
		if s.ConfirmOmissions {
			mark = "[x]"
		}
		controls = append(controls, onboardingControl{mark + " Omitted/uncommitted files will NOT enter worktrees", "omissions", ""})
		if s.Review.Repository.HeadCommit == "" {
			controls = append(controls, onboardingControl{"Create selected baseline / Retry same request", "baseline", ""})
		} else {
			controls = append(controls, onboardingControl{"Use committed content and save workspace", "save", ""})
		}
		return append(controls, onboardingControl{"Refresh review (discard selection)", "review", ""}, onboardingControl{"Cancel review", "cancel", ""})
	}

	if s.AddingWorkspaces {
		controls := []onboardingControl{}
		for _, repo := range s.Repositories {
			mark := "[ ]"
			if s.Selected != nil && s.Selected[repo.Path] {
				mark = "[x]"
			}
			tags := ""
			if hasAgentsMD(repo.Path) || repo.HasSwarm {
				tags += " · [AGENTS.md]"
			}
			if p.model.CWD != "" && filepath.Clean(repo.Path) == filepath.Clean(p.model.CWD) {
				tags += " · [launch folder]"
			}
			label := fmt.Sprintf("%s %s%s", mark, repo.Path, tags)
			controls = append(controls, onboardingControl{label: label, action: "toggle_workspace", path: repo.Path})
		}
		controls = append(controls,
			onboardingControl{label: "[ + Create a new folder… ]", action: "create_folder"},
			onboardingControl{label: "[ Add Selected Workspaces & Finish ]", action: "finish_workspaces"},
			onboardingControl{label: "[ Back ]", action: "back_to_choice"},
		)
		return controls
	}

	if s.ProjectNamed && strings.TrimSpace(s.ProjectName) != "" {
		projName := strings.TrimSpace(s.ProjectName)
		return []onboardingControl{
			{label: fmt.Sprintf("[ Add workspaces into %s? ]", projName), action: "open_workspace_menu"},
			{label: "[ Skip to Talk to Swarm ]", action: "skip_to_swarm"},
		}
	}

	if s.ChoosingRepository {
		controls := []onboardingControl{}
		for _, repo := range s.Repositories {
			controls = append(controls, onboardingControl{"Git repo: " + repo.Path, "repository", repo.Path})
		}
		return append(controls,
			onboardingControl{"Enter repository path", "edit", ""},
			onboardingControl{"[ Add Selected Workspaces & Finish ]", "save", ""},
			onboardingControl{"[ Continue without Workspaces (Skip) ]", "skip", ""},
			onboardingControl{"Refresh repository list", "discover", ""},
			onboardingControl{"Back", "cancel", ""},
		)
	}

	primary := onboardingControl{"Inspect selected folder", "inspect", ""}
	if s.Error != "" {
		primary.label = "Retry selected folder"
	}
	r := s.Repository
	if r != nil {
		switch {
		case r.State == "ready" && r.ContentReady:
			primary = onboardingControl{"Open workspace and finish setup", "save", ""}
		case r.CanSetup && !r.NeedsReview, r.State == "directory_missing":
			primary = onboardingControl{"Set up Git and create workspace", "consent", ""}
		case r.NeedsReview:
			primary = onboardingControl{"Review files and finish Git setup", "review", ""}
			if r.State == "ready" {
				primary.label = "Review uncommitted files and open workspace"
			}
		}
	}
	controls := []onboardingControl{}
	if s.WorkspacePath != "" {
		controls = append(controls, primary)
	}
	controls = append(controls, onboardingControl{"Create a new project folder (recommended)", "new", ""})
	if s.HomePath != "" {
		controls = append(controls, onboardingControl{"Use home folder", "home", s.HomePath})
	}
	controls = append(controls,
		onboardingControl{"Use an existing Git repository…", "discover", ""},
		onboardingControl{"Select another location", "edit", ""},
		onboardingControl{"[ Add Selected Workspaces & Finish ]", "save", ""},
		onboardingControl{"Skip / Continue without folder", "skip", ""},
		onboardingControl{"Back to Project", "back", ""},
		onboardingControl{"Cancel setup / Exit", "exit", ""})
	return controls
}

func (p *HomePage) handleOnboardingWorkspaceKey(ev *tcell.EventKey) {
	s := &p.onboarding
	if s.PreFinish {
		switch ev.Key() {
		case tcell.KeyEnter:
			p.FinishOnboardingPreFinish()
			return
		case tcell.KeyRune:
			if ev.Rune() == ' ' {
				p.FinishOnboardingPreFinish()
				return
			}
		case tcell.KeyEscape:
			s.PreFinish = false
			s.AddingWorkspaces = false
			s.ActionIndex = 0
			return
		}
		return
	}
	if s.NamingProject {
		p.handleOnboardingProjectFolderKey(ev)
		return
	}
	if s.CreatingFolder {
		switch ev.Key() {
		case tcell.KeyEscape:
			s.CreatingFolder = false
			s.NewFolderPath = ""
			s.Error = ""
			return
		case tcell.KeyCtrlU:
			s.NewFolderPath = ""
			s.Error = ""
			return
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			_, size := utf8.DecodeLastRuneInString(s.NewFolderPath)
			if size > 0 {
				s.NewFolderPath = s.NewFolderPath[:len(s.NewFolderPath)-size]
			}
			s.Error = ""
			return
		case tcell.KeyEnter:
			p.submitNewOnboardingFolder()
			return
		case tcell.KeyRune:
			if unicode.IsPrint(ev.Rune()) {
				s.NewFolderPath += string(ev.Rune())
			}
			s.Error = ""
			return
		}
		return
	}
	if s.EditingPath {
		p.handleOnboardingWorkspaceShortcut(ev)
		return
	}

	controls := p.repositoryControls()
	if len(controls) == 0 {
		return
	}

	if ev.Key() == tcell.KeyTab || ev.Key() == tcell.KeyDown {
		s.ActionIndex = (s.ActionIndex + 1) % len(controls)
		return
	}
	if ev.Key() == tcell.KeyBacktab || ev.Key() == tcell.KeyUp {
		s.ActionIndex = (s.ActionIndex + len(controls) - 1) % len(controls)
		return
	}

	if ev.Key() == tcell.KeyEscape {
		if s.Review != nil || s.SetupConsent || s.AddingWorkspaces || s.ChoosingRepository {
			s.AddingWorkspaces = false
			s.ChoosingRepository = false
			s.ConfirmOmissions = false
			s.BaselineAttempt = nil
			s.Review = nil
			wasConsent := s.SetupConsent
			s.SetupConsent = false
			s.ActionIndex = 0
			s.Error = ""
			if wasConsent && s.ProjectName != "" && s.WorkspacePath == p.onboardingProjectDestination() {
				s.NamingProject = true
			}
		} else if s.WorkspacePath != "" {
			s.WorkspacePath = ""
			s.Repository = nil
			s.ActionIndex = 0
		} else if s.ProjectNamed {
			s.ProjectNamed = false
			p.ShowOnboardingProject("Enter project name.")
		} else {
			p.ShowOnboardingProject("Enter project name.")
		}
		return
	}

	if ev.Key() == tcell.KeyCtrlL || ev.Key() == tcell.KeyCtrlN || ev.Key() == tcell.KeyCtrlS {
		p.handleOnboardingWorkspaceShortcut(ev)
		return
	}

	// 's' or 'S' skips workspace addition directly to Swarm (when not in folder input)
	if !s.AddingWorkspaces && ev.Key() == tcell.KeyRune && (ev.Rune() == 's' || ev.Rune() == 'S') {
		p.submitOnboardingProjectWorkspaces(nil)
		return
	}

	// Space toggles selection on repository items in multi-select mode
	if ev.Key() == tcell.KeyRune && ev.Rune() == ' ' {
		if (s.AddingWorkspaces || s.ChoosingRepository) && s.ActionIndex < len(controls) && (controls[s.ActionIndex].action == "toggle_workspace" || controls[s.ActionIndex].action == "repository") {
			if s.Selected == nil {
				s.Selected = make(map[string]bool)
			}
			path := controls[s.ActionIndex].path
			s.Selected[path] = !s.Selected[path]
			return
		}
	}

	if s.SetupConsent && ev.Key() == tcell.KeyRune && ev.Rune() == 'y' {
		s.ActionIndex = 0
	} else if ev.Key() != tcell.KeyEnter {
		return
	}

	if s.ActionIndex >= len(controls) {
		s.ActionIndex = 0
	}
	c := controls[s.ActionIndex]
	kind := HomeActionKind("")
	switch c.action {
	case "finish_prefinish":
		p.FinishOnboardingPreFinish()
		return
	case "open_workspace_menu":
		s.AddingWorkspaces = true
		s.ChoosingRepository = true
		s.ActionIndex = 0
		s.Error = ""
		p.pendingHomeAction = &HomeAction{Kind: HomeActionDiscoverOnboardingRepositories}
		return
	case "skip_to_swarm", "skip":
		p.submitOnboardingProjectWorkspaces(nil)
		return
	case "toggle_workspace":
		if s.Selected == nil {
			s.Selected = make(map[string]bool)
		}
		s.Selected[c.path] = !s.Selected[c.path]
		return
	case "repository", "home":
		s.ChoosingRepository = false
		s.WorkspacePath = c.path
		s.Repository = nil
		s.Review = nil
		s.ConfirmOmissions = false
		s.BaselineAttempt = nil
		s.ActionIndex = 0
		kind = HomeActionInspectOnboardingRepository
	case "create_folder":
		s.CreatingFolder = true
		s.NewFolderPath = ""
		s.Error = ""
		return
	case "finish_workspaces", "save":
		if s.Review != nil && !s.ConfirmOmissions {
			p.SetOnboardingError("Acknowledge omitted content before continuing.")
			return
		}
		if s.Repository != nil {
			kind = HomeActionCreateOnboardingWorkspace
			p.pendingHomeAction = &HomeAction{Kind: kind, WorkspacePath: s.WorkspacePath}
			return
		}
		var selected []string
		for _, r := range s.Repositories {
			if s.Selected != nil && s.Selected[r.Path] {
				selected = append(selected, r.Path)
			}
		}
		if s.WorkspacePath != "" && (s.Selected == nil || !s.Selected[s.WorkspacePath]) {
			selected = append(selected, s.WorkspacePath)
		}
		p.submitOnboardingProjectWorkspaces(selected)
		return
	case "back_to_choice":
		s.AddingWorkspaces = false
		s.ChoosingRepository = false
		s.ActionIndex = 0
		s.Error = ""
		return
	case "cancel":
		s.AddingWorkspaces = false
		s.ChoosingRepository = false
		s.BaselineAttempt = nil
		s.Review = nil
		s.SetupConsent = false
		s.ConfirmOmissions = false
		s.ActionIndex = 0
		s.Error = ""
		return
	case "discover":
		kind = HomeActionDiscoverOnboardingRepositories
	case "new":
		p.beginOnboardingProject()
		return
	case "consent":
		s.SetupConsent = true
		s.ActionIndex = 0
		s.Error = ""
		s.Status = "Confirm: create this folder if needed, initialize Git and an empty first commit, then open it. No existing files are staged."
		return
	case "inspect":
		kind = HomeActionInspectOnboardingRepository
	case "review":
		kind = HomeActionReviewOnboardingRepository
	case "setup":
		kind = HomeActionSetupOnboardingRepository
		s.SetupConsent = false
	case "baseline":
		if !s.ConfirmOmissions {
			p.SetOnboardingError("Acknowledge omitted content before creating a baseline.")
			return
		}
		kind = HomeActionBaselineOnboardingRepository
	case "file":
		if s.BaselineAttempt != nil {
			p.SetOnboardingError("Retry the same request or refresh review before changing selection.")
			return
		}
		for _, f := range s.Review.Files {
			if f.Path == c.path && f.Selectable {
				s.Selected[c.path] = !s.Selected[c.path]
			}
		}
		return
	case "omissions":
		if s.BaselineAttempt == nil {
			s.ConfirmOmissions = !s.ConfirmOmissions
		}
		return
	case "edit":
		s.ChoosingRepository = false
		s.Review = nil
		s.ConfirmOmissions = false
		s.BaselineAttempt = nil
		p.handleOnboardingWorkspaceShortcut(tcell.NewEventKey(tcell.KeyCtrlL, 0, tcell.ModNone))
		return
	case "back":
		p.ShowOnboardingProject("Enter project name.")
		return
	case "exit":
		p.pendingHomeAction = &HomeAction{Kind: HomeActionKind("exit-onboarding")}
		return
	}
	s.Pending = true
	s.Error = ""
	s.Status = "Waiting for daemon acknowledgement..."
	p.pendingHomeAction = &HomeAction{Kind: kind, WorkspacePath: s.WorkspacePath}
}

func (p *HomePage) submitNewOnboardingFolder() {
	s := &p.onboarding
	folder := strings.TrimSpace(s.NewFolderPath)
	if folder == "" {
		s.Error = "Folder path cannot be empty."
		return
	}
	if strings.HasPrefix(folder, "~/") || folder == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			if folder == "~" {
				folder = home
			} else {
				folder = filepath.Join(home, folder[2:])
			}
		}
	}
	absPath, err := filepath.Abs(folder)
	if err != nil {
		s.Error = fmt.Sprintf("Invalid path: %v", err)
		return
	}
	if err := os.MkdirAll(absPath, 0755); err != nil {
		s.Error = fmt.Sprintf("Failed to create directory: %v", err)
		return
	}
	absPath = filepath.Clean(absPath)
	has := false
	for _, r := range s.Repositories {
		if filepath.Clean(r.Path) == absPath {
			has = true
			break
		}
	}
	if !has {
		entry := client.WorkspaceDiscoverEntry{
			Path:      absPath,
			Name:      filepath.Base(absPath),
			IsGitRepo: false,
			HasSwarm:  hasAgentsMD(absPath),
		}
		s.Repositories = append([]client.WorkspaceDiscoverEntry{entry}, s.Repositories...)
	}
	if s.Selected == nil {
		s.Selected = make(map[string]bool)
	}
	s.Selected[absPath] = true
	s.CreatingFolder = false
	s.NewFolderPath = ""
	s.Error = ""
	s.Status = fmt.Sprintf("Created folder: %s", absPath)
}

func (p *HomePage) submitOnboardingProjectWorkspaces(paths []string) {
	s := &p.onboarding
	name := strings.TrimSpace(s.ProjectName)
	if name == "" {
		s.Error = "Project name is required."
		return
	}
	s.Pending = true
	s.Error = ""
	if len(paths) > 0 {
		s.Personalizing = true
		s.Status = fmt.Sprintf("Personalizing %s...", name)
	} else {
		s.Personalizing = false
		s.Status = "Creating project and completing setup…"
	}
	p.pendingHomeAction = &HomeAction{
		Kind:           HomeActionCreateOnboardingProject,
		ProjectName:    name,
		WorkspacePaths: paths,
	}
}

func (p *HomePage) SetOnboardingRepositories(entries []client.WorkspaceDiscoverEntry) {
	s := &p.onboarding
	s.Repositories = nil

	if strings.TrimSpace(s.ProjectName) == "" {
		candidates := make([]client.WorkspaceDiscoverEntry, 0, len(entries)+len(p.model.Directories))
		for _, directory := range p.model.Directories {
			if directory.HasGit {
				candidates = append(candidates, client.WorkspaceDiscoverEntry{Path: directory.ResolvedPath, Name: directory.Name, IsGitRepo: true})
			}
		}
		candidates = append(candidates, entries...)
		seen := map[string]bool{}
		for _, entry := range candidates {
			if entry.IsGitRepo && entry.Path != "" && !seen[entry.Path] {
				s.Repositories = append(s.Repositories, entry)
				seen[entry.Path] = true
			}
		}
		s.Pending = false
		s.ChoosingRepository = true
		s.ActionIndex = 0
		s.Error = ""
		s.Status = "Select a repository to verify it, or press s to skip."
		if len(s.Repositories) == 0 {
			s.Status = "No Git repositories found. Enter a repository path, or press s to continue without one."
		}
		return
	}

	candidates := make([]client.WorkspaceDiscoverEntry, 0, len(entries)+len(p.model.Directories)+1)
	for _, directory := range p.model.Directories {
		candidates = append(candidates, client.WorkspaceDiscoverEntry{
			Path:      directory.ResolvedPath,
			Name:      directory.Name,
			IsGitRepo: directory.HasGit,
			HasSwarm:  hasAgentsMD(directory.ResolvedPath),
		})
	}
	candidates = append(candidates, entries...)
	if p.model.CWD != "" {
		candidates = append(candidates, client.WorkspaceDiscoverEntry{
			Path:      p.model.CWD,
			Name:      filepath.Base(p.model.CWD),
			IsGitRepo: p.model.WorkspaceSetupHasGit,
			HasSwarm:  hasAgentsMD(p.model.CWD),
		})
	}
	seen := map[string]bool{}
	var withAgentsMD []client.WorkspaceDiscoverEntry
	var launchFolder []client.WorkspaceDiscoverEntry
	var others []client.WorkspaceDiscoverEntry

	for _, entry := range candidates {
		entry.Path = filepath.Clean(strings.TrimSpace(entry.Path))
		if entry.Path == "" || seen[entry.Path] {
			continue
		}
		seen[entry.Path] = true
		if hasAgentsMD(entry.Path) {
			entry.HasSwarm = true
		}
		if entry.HasSwarm {
			withAgentsMD = append(withAgentsMD, entry)
		} else if p.model.CWD != "" && entry.Path == filepath.Clean(p.model.CWD) {
			launchFolder = append(launchFolder, entry)
		} else {
			others = append(others, entry)
		}
	}
	s.Repositories = append(s.Repositories, withAgentsMD...)
	s.Repositories = append(s.Repositories, launchFolder...)
	s.Repositories = append(s.Repositories, others...)
	s.Pending = false
	s.ActionIndex = 0
	s.Error = ""
	if s.AddingWorkspaces {
		s.Status = "Select workspaces to add, or space to toggle selection."
	}
}

func (p *HomePage) SetOnboardingRepository(r client.OnboardingRepository) {
	p.onboarding.Repository = &r
	p.onboarding.Pending = false
	p.onboarding.Error = ""
	p.onboarding.WorkspacePath = r.Path
	p.onboarding.Status = r.Message
	p.onboarding.ActionIndex = 0
}

func (p *HomePage) SetOnboardingReview(r client.OnboardingReview) {
	p.SetOnboardingRepository(r.Repository)
	p.onboarding.Review = &r
	p.onboarding.Selected = map[string]bool{}
	p.onboarding.ConfirmOmissions = false
	p.onboarding.BaselineAttempt = nil
	p.onboarding.Status = r.Warning
}

func (p *HomePage) OnboardingCommittedOnly() bool { return p.onboarding.ConfirmOmissions }

func (p *HomePage) OnboardingBaselineRequest() client.OnboardingBaseline {
	s := &p.onboarding
	if s.BaselineAttempt != nil {
		return *s.BaselineAttempt
	}
	if s.Review == nil {
		return client.OnboardingBaseline{}
	}
	req := client.OnboardingBaseline{Path: s.Review.Repository.Path, ExpectedResolvedPath: s.Review.Repository.Path, ReviewDigest: s.Review.Digest, SelectedPaths: []string{}, ConfirmBaseline: true, ConfirmOmissions: s.ConfirmOmissions}
	for _, f := range s.Review.Files {
		if s.Selected[f.Path] {
			req.SelectedPaths = append(req.SelectedPaths, f.Path)
		}
	}
	s.BaselineAttempt = &req
	return req
}

func (p *HomePage) ClearOnboardingReview() { p.onboarding.Review = nil }

var personalizingSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (p *HomePage) drawOnboardingWorkspace(s tcell.Screen, content Rect) {
	st := &p.onboarding
	if st.PreFinish {
		p.drawOnboardingPreFinish(s, content)
		return
	}
	if st.Personalizing {
		p.drawOnboardingPersonalizing(s, content)
		return
	}

	if st.CreatingFolder {
		DrawText(s, content.X+1, content.Y+2, content.W-2, p.theme.Text, "Create new workspace folder")
		fieldRect := Rect{X: content.X, Y: content.Y + 3, W: content.W, H: 3}
		DrawBox(s, fieldRect, p.theme.BorderActive)
		val := st.NewFolderPath
		valStyle := p.theme.Primary
		if val == "" {
			val = "Enter folder path (e.g. ~/my-project)..."
			valStyle = p.theme.TextMuted
		}
		DrawText(s, fieldRect.X+2, fieldRect.Y+1, fieldRect.W-4, valStyle, clampTail(val, fieldRect.W-4))
		DrawText(s, content.X+1, content.Y+7, content.W-2, p.theme.TextMuted, "Directory will be created on disk if it does not exist.")
		DrawText(s, content.X+1, content.Y+content.H-1, content.W-2, p.theme.Primary, "Enter create and select · Esc cancel")
		return
	}

	controls := p.repositoryControls()
	if st.ProjectNamed && st.ProjectName != "" {
		if st.AddingWorkspaces {
			projName := strings.TrimSpace(st.ProjectName)
			DrawText(s, content.X+1, content.Y+1, content.W-2, p.theme.TextMuted, fmt.Sprintf("Workspaces for %s · Space toggle · Enter on Finish", projName))
		} else {
			DrawText(s, content.X+1, content.Y+1, content.W-2, p.theme.TextMuted, fmt.Sprintf("Project: %s", st.ProjectName))
		}
	} else {
		DrawText(s, content.X, content.Y, content.W, p.theme.TextMuted, clampEllipsis("Runtime account: "+st.RuntimeAccount+" (not terminal identity)", content.W))
		DrawText(s, content.X, content.Y+1, content.W, p.theme.Primary, clampTail(st.WorkspacePath, content.W))
		if st.NamingProject {
			p.drawOnboardingProjectFolder(s, content)
			return
		}
		if st.WorkspacePath == "" {
			DrawText(s, content.X, content.Y+1, content.W, p.theme.Text, "A separate project folder is recommended; home is also supported.")
		}
		if st.EditingPath {
			DrawText(s, content.X, content.Y+3, content.W, p.theme.Text, "Edit path: Enter select and verify · Esc cancel · Ctrl+U clear")
			return
		}
		if st.ChoosingRepository {
			DrawText(s, content.X, content.Y+2, content.W, p.theme.TextMuted, fmt.Sprintf("Git repositories · %d found · ↑/↓ scroll · s skip", len(st.Repositories)))
		}
	}

	height := maxInt(1, content.H-3)
	start := maxInt(0, st.ActionIndex-height+1)
	for i := start; i < len(controls) && i < start+height; i++ {
		prefix := "  "
		style := p.theme.Text
		if i == st.ActionIndex {
			prefix = "› "
			style = p.theme.Primary
		}
		DrawText(s, content.X, content.Y+3+i-start, content.W, style, clampEllipsis(prefix+controls[i].label, content.W))
	}
}

func (p *HomePage) drawOnboardingPersonalizing(s tcell.Screen, content Rect) {
	st := &p.onboarding
	frame := personalizingSpinnerFrames[(st.Tick/2)%len(personalizingSpinnerFrames)]
	projName := strings.TrimSpace(st.ProjectName)
	if projName == "" {
		projName = "your project"
	}

	cardH := minInt(11, maxInt(6, content.H-2))
	cardRect := Rect{X: content.X + 1, Y: content.Y + 1, W: content.W - 2, H: cardH}
	DrawBox(s, cardRect, p.theme.BorderActive)

	title := fmt.Sprintf(" %s PERSONALIZING %s ", frame, strings.ToUpper(projName))
	DrawText(s, cardRect.X+2, cardRect.Y, cardRect.W-4, p.theme.Primary.Bold(true), title)

	pct := (st.Tick * 7) % 100
	if pct < 20 {
		pct = 20
	}
	barWidth := maxInt(10, cardRect.W-16)
	filled := (pct * barWidth) / 100
	bar := "[" + strings.Repeat("■", filled) + strings.Repeat("·", barWidth-filled) + "]"
	DrawText(s, cardRect.X+3, cardRect.Y+2, cardRect.W-6, p.theme.Primary, fmt.Sprintf("%s %3d%%", bar, pct))

	step1Style := p.theme.Success
	step2Style := p.theme.Success
	step3Style := p.theme.Primary
	step4Style := p.theme.TextMuted
	step3Icon := frame
	if st.Tick > 6 {
		step3Style = p.theme.Success
		step3Icon = "✓"
		step4Style = p.theme.Primary
	}
	if cardH >= 7 {
		DrawText(s, cardRect.X+3, cardRect.Y+4, cardRect.W-6, step1Style, "✓ Linked workspaces indexed & mapped")
	}
	if cardH >= 8 {
		DrawText(s, cardRect.X+3, cardRect.Y+5, cardRect.W-6, step2Style, "✓ AGENTS.md guidelines and rules extracted")
	}
	if cardH >= 9 {
		DrawText(s, cardRect.X+3, cardRect.Y+6, cardRect.W-6, step3Style, fmt.Sprintf("%s Synthesizing PROJECT.md with AI Router", step3Icon))
	}
	if cardH >= 10 {
		DrawText(s, cardRect.X+3, cardRect.Y+7, cardRect.W-6, step4Style, "· Priming autonomous agent orchestrator")
	}

	if content.H > cardH+2 {
		DrawText(s, content.X+1, cardRect.Y+cardRect.H+1, content.W-2, p.theme.TextMuted, "Configuring project context… Preparing your launch screen.")
	}
}

func (p *HomePage) drawOnboardingPreFinish(s tcell.Screen, content Rect) {
	st := &p.onboarding
	projName := strings.TrimSpace(st.PreFinishProjectName)
	if projName == "" {
		projName = strings.TrimSpace(st.ProjectName)
	}
	if projName == "" {
		projName = "Project"
	}

	cardH := minInt(11, maxInt(6, content.H-2))
	cardRect := Rect{X: content.X + 1, Y: content.Y + 1, W: content.W - 2, H: cardH}
	DrawBox(s, cardRect, p.theme.BorderActive)

	banner := " ✦ ALL SYSTEMS ONLINE · PROJECT READY ✦ "
	if cardRect.W < 46 {
		banner = " ✦ PROJECT READY ✦ "
	}
	DrawText(s, cardRect.X+2, cardRect.Y, cardRect.W-4, p.theme.Success.Bold(true), banner)

	DrawText(s, cardRect.X+3, cardRect.Y+2, 14, p.theme.TextMuted, "⚡ Project    :")
	DrawText(s, cardRect.X+18, cardRect.Y+2, cardRect.W-20, p.theme.Primary.Bold(true), projName)

	wsSummary := "Standalone (No workspaces attached)"
	if len(st.PreFinishWorkspaces) == 1 {
		wsSummary = "1 workspace linked (" + filepath.Base(st.PreFinishWorkspaces[0]) + ")"
	} else if len(st.PreFinishWorkspaces) > 1 {
		wsSummary = fmt.Sprintf("%d workspaces linked and active", len(st.PreFinishWorkspaces))
	}
	if cardH >= 7 {
		DrawText(s, cardRect.X+3, cardRect.Y+3, 14, p.theme.TextMuted, "📁 Workspaces :")
		DrawText(s, cardRect.X+18, cardRect.Y+3, cardRect.W-20, p.theme.Text, clampTail(wsSummary, cardRect.W-20))
	}
	if cardH >= 8 {
		DrawText(s, cardRect.X+3, cardRect.Y+4, 14, p.theme.TextMuted, "✓ Guidelines :")
		DrawText(s, cardRect.X+18, cardRect.Y+4, cardRect.W-20, p.theme.Success, "PROJECT.md configured & rules primed")
	}
	if cardH >= 9 {
		DrawText(s, cardRect.X+3, cardRect.Y+5, 14, p.theme.TextMuted, "🤖 Agent      :")
		DrawText(s, cardRect.X+18, cardRect.Y+5, cardRect.W-20, p.theme.Secondary, "Swarm orchestrator ready for commands")
	}

	if cardH >= 10 {
		DrawHLine(s, cardRect.X+1, cardRect.Y+cardH-3, cardRect.W-2, p.theme.Border)
	}

	btnY := cardRect.Y + cardH - 2
	if btnY <= cardRect.Y+5 && cardH < 8 {
		btnY = cardRect.Y + cardH - 1
	}
	launchBtn := fmt.Sprintf("›› [ Launch %s & Talk to Swarm → ] ‹‹", projName)
	DrawText(s, cardRect.X+3, btnY, cardRect.W-6, p.theme.Primary.Bold(true), clampEllipsis(launchBtn, cardRect.W-6))

	if content.H > cardH+2 {
		secsLeft := maxInt(1, (20-st.PreFinishTicks+3)/4)
		hint := fmt.Sprintf("Press Enter or Space to launch · Auto-launching in %ds", secsLeft)
		DrawText(s, content.X+1, cardRect.Y+cardRect.H+1, content.W-2, p.theme.TextMuted, hint)
	}
}
