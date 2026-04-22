package model

import (
	"os"
	"path/filepath"
	"strings"

	"swarm-refactor/swarmtui/internal/buildinfo"
	"swarm-refactor/swarmtui/internal/client"
)

type Workspace struct {
	Name        string
	Path        string
	Directories []string
	ThemeID     string
	Icon        string
	Active      bool
}

type DirectoryItem struct {
	Name           string
	Path           string
	ResolvedPath   string
	Branch         string
	DirtyCount     int
	StagedCount    int
	ModifiedCount  int
	UntrackedCount int
	ConflictCount  int
	AheadCount     int
	BehindCount    int
	Upstream       string
	HasGit         bool
	AgentsToken    string
	IsWorkspace    bool
}

type SessionSummary struct {
	ID                     string
	WorkspacePath          string
	WorkspaceName          string
	Title                  string
	Mode                   string
	Metadata               map[string]any
	PendingPermissionCount int
	Lifecycle              *client.SessionLifecycleSnapshot
	Preference             client.ModelPreference
	WorktreeEnabled        bool
	WorktreeRootPath       string
	WorktreeBaseBranch     string
	WorktreeBranch         string
	UpdatedAgo             string
	Depth                  int
}

type BackgroundSessionSummary struct {
	ChildSessionID      string
	ParentSessionID     string
	ParentTitle         string
	ChildTitle          string
	TargetKind          string
	TargetName          string
	Status              string
	PendingPermissions  int
	WorkspacePath       string
	WorkspaceName       string
	CWD                 string
	WorktreeMode        string
	WorktreeRootPath    string
	WorktreeBranch      string
	WorktreeBaseBranch  string
	LaunchMode          string
	Instructions        string
	LastActivitySnippet string
	Background          bool
	LastUpdatedAtUnixMS int64
	StartedAtUnixMS     int64
}

type HomeModel struct {
	Title                       string
	Version                     string
	UpdateStatus                *client.UpdateStatus
	ActivePlan                  string
	ServerURL                   string
	ServerMode                  string
	BypassPermissions           bool
	CWD                         string
	AuthConfigured              bool
	AuthType                    string
	AuthLast4                   string
	ModelProvider               string
	ModelName                   string
	ThinkingLevel               string
	ServiceTier                 string
	ContextMode                 string
	ActiveAgent                 string
	ActiveAgentExecutionSetting string
	ActiveAgentExitPlanMode     bool
	ActiveAgentRuntimeKnown     bool
	Subagents                   []string
	ContextWindow               int
	RuleCount                   int
	SkillCount                  int
	WorktreesEnabled            bool
	Workspaces                  []Workspace
	Directories                 []DirectoryItem
	PromptHint                  string
	QuickActions                []string
	HintLine                    string
	TipLine                     string
	RecentSessions              []SessionSummary
	BackgroundSessions          []BackgroundSessionSummary
}

func EmptyHome() HomeModel {
	return HomeModel{
		Title:        "Swarm",
		Version:      buildinfo.DisplayVersion(),
		UpdateStatus: nil,
		ActivePlan:   "",
		ServerMode:   "local",
		PromptHint:   "",
		QuickActions: nil,
		HintLine:     "",
		TipLine:      "",
	}
}

func MockHome() HomeModel {
	root := mockWorkspaceRoot()
	swarmPath := root
	boxPath := filepath.Join(root, "swarmd")
	sdkPath := filepath.Join(root, "sdk")
	infraPath := filepath.Join(root, "infra")
	docsPath := filepath.Join(root, "docs")
	playgroundPath := filepath.Join(root, "playground")

	return HomeModel{
		Title:                       "Swarm",
		Version:                     buildinfo.DisplayVersion(),
		UpdateStatus:                &client.UpdateStatus{CurrentVersion: buildinfo.DisplayVersion(), LatestVersion: "v0.2.0", UpdateAvailable: true, CheckedAtUnixMS: 1735689600000},
		ActivePlan:                  "Core Platform",
		ServerURL:                   "http://127.0.0.1:7781",
		ServerMode:                  "local",
		CWD:                         ".",
		ModelProvider:               "codex",
		ModelName:                   "gpt-5.4",
		ThinkingLevel:               "xhigh",
		ServiceTier:                 "",
		ContextMode:                 "",
		ActiveAgent:                 "swarm",
		ActiveAgentExecutionSetting: "",
		ActiveAgentExitPlanMode:     true,
		ActiveAgentRuntimeKnown:     true,
		Subagents:                   []string{"clone", "explorer", "memory"},
		ContextWindow:               200000,
		Workspaces: []Workspace{
			{Name: "swarm", Path: displayPath(swarmPath), Icon: "*", Active: true},
			{Name: "swarm-box", Path: displayPath(boxPath), Icon: "+", Active: false},
			{Name: "sdk", Path: displayPath(sdkPath), Icon: "-", Active: false},
			{Name: "infra", Path: displayPath(infraPath), Icon: "#", Active: false},
			{Name: "docs", Path: displayPath(docsPath), Icon: "=", Active: false},
			{Name: "playground", Path: displayPath(playgroundPath), Icon: "~", Active: false},
		},
		Directories: []DirectoryItem{
			{Name: "swarm", Path: displayPath(swarmPath), ResolvedPath: swarmPath, Branch: "main", DirtyCount: 2, StagedCount: 1, ModifiedCount: 1, AheadCount: 1, Upstream: "origin/main", HasGit: true, AgentsToken: "agents", IsWorkspace: true},
			{Name: "swarm-box", Path: displayPath(boxPath), ResolvedPath: boxPath, Branch: "refactor/box-sync", DirtyCount: 4, StagedCount: 2, ModifiedCount: 1, UntrackedCount: 1, BehindCount: 2, Upstream: "origin/refactor/box-sync", HasGit: true, AgentsToken: "agents", IsWorkspace: true},
			{Name: "sdk", Path: displayPath(sdkPath), ResolvedPath: sdkPath, Branch: "feat/sdk-streaming", DirtyCount: 1, ModifiedCount: 1, Upstream: "origin/feat/sdk-streaming", HasGit: true, AgentsToken: "none", IsWorkspace: true},
			{Name: "infra", Path: displayPath(infraPath), ResolvedPath: infraPath, Branch: "ops/latency-tracing", DirtyCount: 0, Upstream: "origin/ops/latency-tracing", HasGit: true, AgentsToken: "none", IsWorkspace: true},
			{Name: "docs", Path: displayPath(docsPath), ResolvedPath: docsPath, Branch: "docs/architecture-v2", DirtyCount: 3, StagedCount: 1, ModifiedCount: 1, UntrackedCount: 1, Upstream: "origin/docs/architecture-v2", HasGit: true, AgentsToken: "none", IsWorkspace: true},
			{Name: "playground", Path: displayPath(playgroundPath), ResolvedPath: playgroundPath, Branch: "tui/control-plane", DirtyCount: 7, StagedCount: 2, ModifiedCount: 3, UntrackedCount: 1, ConflictCount: 1, AheadCount: 3, BehindCount: 1, Upstream: "origin/tui/control-plane", HasGit: true, AgentsToken: "agents+claude", IsWorkspace: true},
		},
		PromptHint:   "",
		QuickActions: []string{"Agent: swarm", "Model: gpt-5.4", "Thinking: xhigh"},
		HintLine:     "Type /help for commands",
		TipLine:      "/workspace  •  /models  •  /auth",
		RecentSessions: []SessionSummary{
			{ID: "session-1", WorkspacePath: swarmPath, WorkspaceName: "swarm", Title: "Refactor provider handlers for codex/google/claude", UpdatedAgo: "4m"},
			{ID: "session-2", WorkspacePath: swarmPath, WorkspaceName: "swarm", Title: "Investigate TUI render pressure with subagents", UpdatedAgo: "19m"},
			{ID: "session-3", WorkspacePath: swarmPath, WorkspaceName: "swarm", Title: "Design Pebble-backed store abstraction", UpdatedAgo: "1h"},
			{ID: "session-4", WorkspacePath: swarmPath, WorkspaceName: "swarm", Title: "Cross-device session topology notes", UpdatedAgo: "2h"},
			{ID: "session-5", WorkspacePath: swarmPath, WorkspaceName: "swarm", Title: "Box mode auth and vault flow cleanup", UpdatedAgo: "5h"},
			{ID: "session-6", WorkspacePath: swarmPath, WorkspaceName: "swarm", Title: "Top bar workspace controls", UpdatedAgo: "8h"},
			{ID: "session-7", WorkspacePath: swarmPath, WorkspaceName: "swarm", Title: "Benchmark tcell render loop under load", UpdatedAgo: "12h"},
			{ID: "session-8", WorkspacePath: swarmPath, WorkspaceName: "swarm", Title: "Agent stream fanout and latency notes", UpdatedAgo: "1d"},
		},
	}
}

func mockWorkspaceRoot() string {
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Clean(filepath.Join(home, "swarm"))
	}
	return filepath.Clean(filepath.Join(string(filepath.Separator), "workspace", "swarm"))
}

func displayPath(path string) string {
	clean := filepath.Clean(path)
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		home = filepath.Clean(home)
		if clean == home {
			return "~"
		}
		prefix := home + string(filepath.Separator)
		if strings.HasPrefix(clean, prefix) {
			return "~" + string(filepath.Separator) + strings.TrimPrefix(clean, prefix)
		}
	}
	return clean
}
