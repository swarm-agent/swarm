package api

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

// This file holds the completeness side of the MCP / route parity guard:
// every home-reach MCP action and every registered route is either compared
// or listed with the reason it is not, so a new action or route fails until
// it is placed. A change to a route or MCP action must keep these tests
// green; an allowlist entry needs a reason (AGENTS.md).

// mcpHomeRouteSpecs bind the routes of home-reach actions (they run on this
// machine only, through the catalog handler in-process) that this test
// compares with them. They are not reached over the link, so
// mcpRouteSpecs' stale check does not cover them;
// TestMCPRouteParityHomeActions does.
var mcpHomeRouteSpecs = map[string]mcpRouteSpec{
	"POST /v3/projects":       {Func: "handleProjects", Anchors: []string{`=trimmed == ""`, `=r.Method == http.MethodPost`}},
	"PATCH /v3/projects/{id}": {Func: "handleProjects", Anchors: []string{`=len(segments) == 1`, `=r.Method == http.MethodPatch`}},
}

// mcpHomeAction is how a home-reach MCP action relates to an HTTP route: the
// route shape it is compared with (Path: inputs carried in the path), or why
// it is not compared.
type mcpHomeAction struct {
	Shape  string
	Path   []string
	Reason string
}

// mcpHomeTODO exempts a home action whose HTTP counterpart is not compared
// yet.
const mcpHomeTODO = "TODO: home-only; Swarm Control and the Orchestrator run the catalog handler in-process, and its HTTP counterpart (if any) is not derived or compared yet"

// mcpHomeActions lists every home-reach action Swarm Control serves. An
// action only the Orchestrator lists (not ControlCallable) has no MCP
// surface and needs no entry.
var mcpHomeActions = map[string]mcpHomeAction{
	"manage_projects create":            {Shape: "POST /v3/projects"},
	"manage_projects update":            {Shape: "PATCH /v3/projects/{id}", Path: []string{"id", "project_id"}},
	"manage_projects update_task":       {Reason: "TODO: the task PATCH route is the Desktop's record patch (status, plan and delivery facts) and reads none of update_task's priority, group, order or expected_revision; update_task runs its own service in-process, so they are not one operation to compare"},
	"manage_projects flow":              {Reason: mcpHomeTODO},
	"manage_projects list_machines":     {Reason: "reads this home's own linked-machine records (the fleet store); no project route"},
	"manage_projects list_linked_tasks": {Reason: "TODO: reads this home's own records of tasks it sent to linked machines; the home's linked-tasks route is not compared"},
	"manage_projects integrate_task":    {Reason: mcpHomeTODO},
	"manage_projects unarchive_task":    {Reason: mcpHomeTODO},
	"manage_projects delete_task":       {Reason: mcpHomeTODO},
	"manage_projects delete":            {Reason: mcpHomeTODO},
	"manage_projects list_sources":      {Reason: mcpHomeTODO},
	"manage_projects inspect_source":    {Reason: mcpHomeTODO},
	"manage_workflows help":             {Reason: "static guide text: no inputs and no route"},
	"manage_setup permissions":          {Reason: mcpHomeTODO},
	"manage_setup list_keys":            {Reason: mcpHomeTODO},
	"manage_setup list_fleet_keys":      {Reason: mcpHomeTODO},
	"manage_setup list_secrets":         {Reason: mcpHomeTODO},
	"manage_setup set_permissions":      {Reason: mcpHomeTODO},
	"manage_setup create_key":           {Reason: mcpHomeTODO},
	"manage_setup create_client_key":    {Reason: mcpHomeTODO},
	"manage_setup revoke_key":           {Reason: mcpHomeTODO},
	"manage_setup request_fleet_key":    {Reason: mcpHomeTODO},
	"manage_setup revoke_fleet_key":     {Reason: mcpHomeTODO},
	"manage_setup request_provider_key": {Reason: mcpHomeTODO},
	"manage_setup connect_chatgpt":      {Reason: mcpHomeTODO},
	"manage_setup connect_claude_code":  {Reason: mcpHomeTODO},
	"manage_fleet list_machines":        {Reason: "reads this home's own linked-machine records (the fleet store); no project route"},
	"manage_fleet list_linked_tasks":    {Reason: "TODO: reads this home's own records of tasks it sent to linked machines; the home's linked-tasks route is not compared"},
	"manage_custom_agents list":         {Reason: mcpHomeTODO},
	"manage_custom_agents get":          {Reason: mcpHomeTODO},
	"manage_custom_agents define":       {Reason: mcpHomeTODO},
	"manage_custom_agents disable":      {Reason: mcpHomeTODO},
	"manage_themes get":                 {Reason: mcpHomeTODO},
}

// mcpHomeRouteParityAllow are known differences between a compared home
// action and its route, by "tool action field kind" (input_unread: the
// action declares an input the route does not read; route_undeclared: the
// route reads a field the action does not declare). Each needs its reason.
var mcpHomeRouteParityAllow = map[string]string{
	"manage_projects create account_id route_undeclared":          "the route decodes the whole project record (pebblestore.ProjectRecord); a record field, not a create input",
	"manage_projects create created_at route_undeclared":          "the route decodes the whole project record (pebblestore.ProjectRecord); a record field, not a create input",
	"manage_projects create updated_at route_undeclared":          "the route decodes the whole project record (pebblestore.ProjectRecord); a record field, not a create input",
	"manage_projects create creation_hash route_undeclared":       "the route decodes the whole project record (pebblestore.ProjectRecord); a record field, not a create input",
	"manage_projects create creation_request_id route_undeclared": "the route decodes the whole project record (pebblestore.ProjectRecord); a record field, not a create input",
	"manage_projects create id route_undeclared":                  "the route decodes the whole project record (pebblestore.ProjectRecord); a record field, not a create input",
	"manage_projects create context_generation route_undeclared":  "the route decodes the whole project record (pebblestore.ProjectRecord); a record field, not a create input",
	"manage_projects create active_task_ids route_undeclared":     "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects create automation_ids route_undeclared":      "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects create theme_id route_undeclared":            "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects create icon_png_data_url route_undeclared":   "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects create uploaded_media route_undeclared":      "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects create primary_session_id route_undeclared":  "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects update active_task_ids route_undeclared":     "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects update automation_ids route_undeclared":      "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects update theme_id route_undeclared":            "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects update icon_png_data_url route_undeclared":   "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects update primary_session_id route_undeclared":  "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects update project_md input_unread":              "TODO: update writes project_md through its own in-process service; the project PATCH route does not take it",
}

// Purpose:
//   - Requirement: every home-reach action Swarm Control serves is either
//     compared with the HTTP route for the same operation (its declared
//     inputs against the fields the route's source reads, both ways) or
//     listed with the reason it is not; a new home action without an entry
//     fails.
//   - Threat/regression: an MCP action and the API route for the same
//     operation drifting apart (a field one takes and the other ignores) on
//     actions that never cross the link, which the linked forwarding test
//     cannot see.
//   - Boundary/authority: tool.Catalog (Reach, ControlCallable, Inputs)
//     and the route handler source (mcpHomeRouteSpecs).
//   - Narrowest layer: declarations and handler source; no daemon runs.
func TestMCPRouteParityHomeActions(t *testing.T) {
	var drift []string
	matched := map[string]bool{}
	note := func(id, field, kind, detail string) {
		key := id + " " + field + " " + kind
		if _, ok := mcpHomeRouteParityAllow[key]; ok {
			matched[key] = true
			return
		}
		drift = append(drift, fmt.Sprintf("%q: %s", key, detail))
	}
	seen := map[string]bool{}
	usedShapes := map[string]bool{}
	for _, catalog := range tool.Catalog() {
		for _, action := range catalog.Actions {
			if action.Reach != tool.CatalogReachHome || !action.ControlCallable() {
				continue
			}
			id := catalog.Name + " " + action.Action
			seen[id] = true
			entry, ok := mcpHomeActions[id]
			switch {
			case !ok:
				drift = append(drift, id+" is a home-reach MCP action with no mcpHomeActions entry: compare it with its route (Shape) or give the reason it has none")
				continue
			case entry.Shape == "" && strings.TrimSpace(entry.Reason) == "":
				drift = append(drift, id+": an mcpHomeActions entry needs a Shape or a Reason")
				continue
			case entry.Shape == "":
				continue
			}
			usedShapes[entry.Shape] = true
			_, fields, found, err := mcpRouteAccepts(entry.Shape)
			if !found || err != nil {
				drift = append(drift, fmt.Sprintf("%s: route %s: found=%v err=%v", id, entry.Shape, found, err))
				continue
			}
			if len(fields.Unresolved) > 0 {
				drift = append(drift, fmt.Sprintf("%s: route %s: %s", id, entry.Shape, strings.Join(fields.Unresolved, "; ")))
			}
			accepted := map[string]bool{}
			for key := range fields.Query {
				accepted[key] = true
			}
			for key := range fields.Body {
				accepted[key] = true
			}
			declared := map[string]bool{}
			for _, name := range mcpParityInputs(action) {
				declared[name] = true
				if slices.Contains(entry.Path, name) {
					continue
				}
				if !accepted[name] {
					note(id, name, "input_unread", entry.Shape+" does not read "+name)
				}
			}
			for _, key := range mcpParitySortedKeys(accepted) {
				if !declared[key] {
					note(id, key, "route_undeclared", entry.Shape+" reads "+key+", which the action does not declare")
				}
			}
		}
	}
	for _, id := range mcpParitySortedKeys(mcpHomeActions) {
		if !seen[id] {
			drift = append(drift, "mcpHomeActions lists "+id+", which is not a home-reach action Swarm Control serves: remove it")
		}
	}
	for _, shape := range mcpParitySortedKeys(mcpHomeRouteSpecs) {
		if !usedShapes[shape] {
			drift = append(drift, "mcpHomeRouteSpecs entry "+shape+" is compared with no action: remove it")
		}
	}
	for key := range mcpHomeRouteParityAllow {
		if !matched[key] {
			drift = append(drift, fmt.Sprintf("mcpHomeRouteParityAllow entry %q no longer matches: remove it", key))
		}
	}
	sort.Strings(drift)
	if len(drift) > 0 {
		t.Fatalf("home MCP action / HTTP route drift (%d):\n%s", len(drift), strings.Join(drift, "\n"))
	}
}

// mcpRouteParityReached are the route registrations MCP actions reach over
// HTTP, with how: their shapes are in mcpRouteSpecs or mcpHomeRouteSpecs
// (handler source compared) or they serve catalog tools
// (mcpCatalogRoutePrefixes: serveCatalogLinked takes exactly the declared
// inputs).
var mcpRouteParityReached = map[string]string{
	`ProjectsPath -> s.handleProjects`:                   "manage_projects list on a linked machine; mcpRouteSpecs",
	`ProjectsPath + "/" -> s.handleProjects`:             "manage_projects project and task routes; mcpRouteSpecs and mcpHomeRouteSpecs",
	`JobAutomationsPath -> s.handleJobAutomations`:       "manage_workflows list on a linked machine; mcpRouteSpecs",
	`JobAutomationsPath + "/" -> s.handleJobAutomations`: "manage_workflows routes on a linked machine; mcpRouteSpecs",
	`JobDeliveriesPath -> s.handleJobDeliveries`:         "manage_workflows deliveries on a linked machine; mcpRouteSpecs",
	`JobDeliveriesPath + "/" -> s.handleJobDeliveries`:   "registered with the collection; a linked deliveries call reads the collection or one delivery through the workflows route",
	`CatalogActionsPath + "/" -> s.handleCatalogActions`: "catalog tools on a linked machine (serveCatalogLinked)",
	`CatalogLinkedPath + "/" -> s.handleCatalogLinked`:   "catalog tools on a linked machine (serveCatalogLinked)",
	`SetupActionsPath + "/" -> s.handleSetupActions`:     "manage_setup on a linked machine (serveCatalogLinked)",
	`WorkerActionsPath + "/" -> s.handleWorkerActions`:   "manage_workers on a linked machine (serveCatalogLinked)",
	`JobsPath + "/" -> s.handleJobs`:                     "manage_jobs on a linked machine (the catalog-linked jobs route)",
	`WorkersPath -> s.handleWorkers`:                     "registered with the worker routes; no MCP action reads the collection over HTTP",
	`WorkersPath + "/" -> s.handleWorkers`:               "TODO: a linked worker list reads WorkersPath/{id}/runs (limit, cursor) for its summary (compactFleetWorkers); that page is not derived",
}

// mcpRouteParityNoMCPAction are the route registrations no MCP action reaches
// over HTTP: Desktop, console, sync/realtime, auth, onboarding, provider,
// fleet-link and owner routes. Each is the API of a client or of the daemon
// itself; a catalog action runs its service in-process, not through these
// routes. A new MCP action that calls one of them over HTTP must move it to
// mcpRouteParityReached and give its shape an mcpRouteSpecs entry. Frozen:
// a new registration fails TestMCPRouteParityRegistrations until it is
// listed in one place.
var mcpRouteParityNoMCPAction = []string{
	`"/ws" -> s.handleDesktopStream`,
	`"/v1/auth/desktop/session" -> s.handleDesktopLocalSessionBootstrap`,
	`TailscaleOnboardingApprovalPath -> s.handleTailscaleOnboardingApproval`,
	`"/v1/feedback" -> s.handleDesktopFeedback`,
	`"/v1/update/status" -> s.handleUpdateStatus`,
	`"/v1/update/apply" -> s.handleUpdateApply`,
	`"/v1/update/run" -> s.handleUpdateRun`,
	`fleetPath -> s.ownerOnlyScope(s.handleFleet)`,
	`fleetPath + "/" -> s.ownerOnlyScope(s.handleFleet)`,
	`fleetActivityPagePath -> s.handleFleetActivity`,
	`fleetActivityStreamPath -> s.handleFleetActivity`,
	`fleetAgentsPath -> s.handleFleetAgents`,
	`fleetKeysPath -> s.handleFleetKeys`,
	`fleetKeysPath + "/" -> s.handleFleetKeys`,
	`fleetLinkedTasksPath -> s.handleFleetLinkedTasks`,
	`fleetLinkedTaskSessionPath -> s.handleFleetLinkedTasks`,
	`"/v1/memory" -> s.handleMemory`,
	`"/v1/memory/workspace-map" -> s.handleWorkspaceMap`,
	`"/healthz" -> s.handleHealth`,
	`"/readyz" -> s.handleReady`,
	`controlMCPPath -> s.handleControlMCP`,
	`"/v1/remote" -> s.handleRemoteTransport`,
	`"/v1/remote/" -> s.handleRemoteTransport`,
	`"/v1/auth/codex" -> s.ownerOnlyScope(s.handleCodexAuth)`,
	`"/v1/auth/codex/oauth/start" -> s.ownerOnlyScope(s.handleCodexOAuthStart)`,
	`"/v1/auth/codex/oauth/status" -> s.ownerOnlyScope(s.handleCodexOAuthStatus)`,
	`"/v1/auth/codex/oauth/complete" -> s.ownerOnlyScope(s.handleCodexOAuthComplete)`,
	`"/v1/auth/credentials" -> s.ownerOnlyScope(s.handleAuthCredentials)`,
	`"/v1/claudecode/accounts" -> s.ownerOnlyScope(s.handleClaudeCodeAccounts)`,
	`"/v1/auth/credentials/verify" -> s.ownerOnlyScope(s.handleAuthCredentialVerify)`,
	`"/v1/auth/credentials/active" -> s.ownerOnlyScope(s.handleAuthCredentialActive)`,
	`"/v1/auth/credentials/delete" -> s.ownerOnlyScope(s.handleAuthCredentialDelete)`,
	`"/v1/auth/attach/rotate" -> s.ownerOnlyScope(s.handleAttachRotate)`,
	`clientSessionsPath -> s.ownerOnlyScope(s.handleClientSessions)`,
	`clientSessionsPath + "/" -> s.ownerOnlyScope(s.handleClientSessions)`,
	`"/v1/vault" -> s.ownerOnlyScope(s.handleVaultStatus)`,
	`"/v1/vault/enable" -> s.ownerOnlyScope(s.handleVaultEnable)`,
	`"/v1/vault/unlock" -> s.ownerOnlyScope(s.handleVaultUnlock)`,
	`"/v1/vault/lock" -> s.ownerOnlyScope(s.handleVaultLock)`,
	`"/v1/vault/disable" -> s.ownerOnlyScope(s.handleVaultDisable)`,
	`"/v1/vault/export" -> s.ownerOnlyScope(s.handleVaultExport)`,
	`"/v1/vault/import" -> s.ownerOnlyScope(s.handleVaultImport)`,
	`"/me" -> s.handleMe`,
	`"/v1/me" -> s.handleMe`,
	`"/v1/onboarding" -> s.handleOnboarding`,
	`"/v1/onboarding/provider/credential" -> s.ownerOnlyScope(s.handleOnboardingProviderCredential)`,
	`"/v3/signals" -> s.handleSignals`,
	`"/v3/box/summary" -> s.handleBoxSummary`,
	`"/v3/box/busy" -> s.handleBoxBusy`,
	`"/v3/box/drain" -> s.handleBoxDrain`,
	`"/v3/runs" -> s.handleRuns`,
	`"/v3/outbox" -> s.handleOutbox`,
	`ProposalsPath -> s.handleProposals`,
	`ProposalsPath + "/" -> s.handleProposals`,
	`LimitRetryPath -> s.handleLimitRetry`,
	`"/v3/signals/sinks" -> s.handleSignalSinks`,
	`"/v3/signals/sinks/" -> s.handleSignalSinks`,
	`"/v1/secrets" -> s.handleSecrets`,
	`"/v1/secrets/" -> s.handleSecrets`,
	`"/v3/secrets/status" -> s.handleSecretStatus`,
	`SandboxEnvironmentsPath -> s.handleSandboxEnvironments`,
	`SandboxEnvironmentsPath + "/" -> s.handleSandboxEnvironments`,
	`"/v1/account/username" -> s.handleAccountUsername`,
	`"/v1/account/avatar" -> s.handleAccountAvatar`,
	`"/v1/account/team/upgrade" -> s.handleAccountTeamUpgrade`,
	`AgentModelSettingsPath -> s.handleAgentModelSettings`,
	`AgentModelSettingsPath + "/restore-defaults" -> s.handleRestoreAgentModelDefaults`,
	`"/v1/swarm/targets" -> s.handleSwarmTargets`,
	`"/v1/swarm/target/current" -> s.handleSwarmCurrentTarget`,
	`"/v1/swarm/target/select" -> s.handleSwarmSelectTarget`,
	`"/v1/swarm/groups" -> s.handleSwarmGroups`,
	`"/v1/swarm/groups/upsert" -> s.handleSwarmGroupUpsert`,
	`"/v1/swarm/groups/current" -> s.handleSwarmCurrentGroup`,
	`"/v1/swarm/groups/members/delete" -> s.handleSwarmGroupMemberDelete`,
	`"/v1/swarm/topology" -> s.handleSwarmTopologySnapshot`,
	`"/v1/swarm/topology/workspace-bindings" -> s.handleSwarmTopologyWorkspaceBindings`,
	`"/v3/agents" -> s.handleCustomAgents`,
	`"/v3/agents/" -> s.handleCustomAgents`,
	`"/v2/custom-tools" -> s.agentConfigScope(s.handleCustomToolsV2)`,
	`"/v2/custom-tools/" -> s.agentConfigScope(s.handleCustomToolByNameV2)`,
	`"/v2/agents" -> s.agentConfigScope(s.handleAgentsV2)`,
	`"/v2/agents/defaults/restore" -> s.agentConfigScope(s.handleAgentDefaultsRestoreV2)`,
	`"/v2/agents/defaults/reset" -> s.agentConfigScope(s.handleAgentDefaultsResetV2)`,
	`"/v2/agents/" -> s.agentConfigScope(s.handleAgentByNameV2)`,
	`"/v1/codex/account/usage" -> s.handleCodexAccountUsage`,
	`"/v1/claudecode/usage" -> s.handleClaudeCodeUsage`,
	`"/v1/codex/account/reset-credits" -> s.handleCodexResetCredits`,
	`"/v1/codex/account/reset-credits/consume" -> s.handleCodexConsumeResetCredit`,
	`"/v1/image/providers" -> s.handleImageGenerationProviders`,
	`"/v1/media/settings/catalog" -> s.handleMediaSettingsCatalog`,
	`"/v1/image/generations" -> s.handleImageGenerations`,
	`"/v1/image/assets" -> s.handleImageAssets`,
	`"/v1/image/storage/reveal" -> s.handleImageStorageReveal`,
	`"/v1/model" -> s.handleModelPreference`,
	`"/v1/model-profiles" -> s.handleModelProfiles`,
	`"/v1/model-profiles/default" -> s.handleModelProfileDefault`,
	`"/v1/model-profiles/bulk-delete" -> s.handleModelProfilesBulkDelete`,
	`"/v1/model-profiles/" -> s.handleModelProfileByID`,
	`"/v1/model/catalog" -> s.handleModelCatalog`,
	`"/v1/model/catalog/check" -> s.handleModelCatalogCheck`,
	`"/v1/models/favorites" -> s.handleModelFavorites`,
	`"/v1/models/favorites/delete" -> s.handleModelFavoriteDelete`,
	`"/v1/providers" -> s.handleProviders`,
	`"/v1/stt/transcribe" -> s.handleSTTTranscribe`,
	`"/v1/voice/status" -> s.handleVoiceStatus`,
	`"/v1/voice/profiles" -> s.handleVoiceProfiles`,
	`"/v1/voice/profiles/upsert" -> s.handleVoiceProfileUpsert`,
	`"/v1/voice/profiles/delete" -> s.handleVoiceProfileDelete`,
	`"/v1/voice/config" -> s.handleVoiceConfig`,
	`"/v1/voice/devices" -> s.handleVoiceDevices`,
	`"/v1/voice/test-stt" -> s.handleVoiceTestSTT`,
	`"/v1/ui/settings" -> s.handleUISettings`,
	`TailscaleSettingsPath -> s.handleTailscaleSettings`,
	`TailscaleSettingsApprovePath -> s.handleTailscaleSettingsApprove`,
	`TailscaleSettingsRevokePath -> s.handleTailscaleSettingsRevoke`,
	`"/v1/workspace/resolve" -> s.handleWorkspaceResolve`,
	`"/v1/workspace/select" -> s.handleWorkspaceSelect`,
	`"/v1/workspace/current" -> s.handleWorkspaceCurrent`,
	`"/v1/workspace/list" -> s.handleWorkspaceList`,
	`WorkspaceDefinitionRefreshPath -> s.handleWorkspaceDefinitionRefresh`,
	`"/v1/workspace/overview" -> s.handleWorkspaceOverview`,
	`"/v1/workspace/cwd/resolve" -> s.handleWorkspaceCWDResolve`,
	`"/v1/workspace/discover" -> s.handleWorkspaceDiscover`,
	`"/v1/workspace/browse" -> s.handleWorkspaceBrowse`,
	`"/v1/workspace/video/scan" -> s.handleWorkspaceVideoScan`,
	`"/v1/workspace/video/transcribe" -> s.handleWorkspaceVideoTranscribe`,
	`"/v1/workspace/video/transcribe/status" -> s.handleWorkspaceVideoTranscribeStatus`,
	`"/v1/workspace/video/transcribe/read" -> s.handleWorkspaceVideoTranscribeRead`,
	`"/v1/workspace/video/transcribe/cancel" -> s.handleWorkspaceVideoTranscribeCancel`,
	`"/v1/workspace/audio/transcribe" -> s.handleWorkspaceAudioTranscribe`,
	`"/v1/workspace/audio/transcribe/status" -> s.handleWorkspaceAudioTranscribeStatus`,
	`"/v1/workspace/audio/transcribe/read" -> s.handleWorkspaceAudioTranscribeRead`,
	`"/v1/workspace/audio/transcribe/cancel" -> s.handleWorkspaceAudioTranscribeCancel`,
	`"/v1/workspace/audio/analysis/read" -> s.handleWorkspaceAudioAnalysisRead`,
	`"/v1/workspace/video/storage/reveal" -> s.handleVideoStorageReveal`,
	`"/v1/workspace/video/projects" -> s.handleWorkspaceVideoProjects`,
	`"/v1/workspace/video/projects/fork" -> s.handleWorkspaceVideoProjectFork`,
	`"/v1/workspace/video/threads" -> s.handleWorkspaceVideoThreads`,
	`"/v1/workspace/video/threads/" -> s.handleWorkspaceVideoThread`,
	`"/v1/workspace/image/threads" -> s.handleWorkspaceImageThreads`,
	`"/v1/workspace/image/threads/" -> s.handleWorkspaceImageThread`,
	`"/v1/workspace/folders/create" -> s.handleWorkspaceFolderCreate`,
	`"/v1/workspace/add" -> s.handleWorkspaceAdd`,
	`"/v1/workspace/repository" -> s.handleWorkspaceRepositoryInspect`,
	`"/v1/workspace/repository/setup" -> s.handleWorkspaceRepositorySetup`,
	`"/v1/workspace/repository/review" -> s.handleWorkspaceRepositoryReview`,
	`"/v1/workspace/repository/baseline" -> s.handleWorkspaceRepositoryBaseline`,
	`"/v1/workspace/directories/add" -> s.handleWorkspaceDirectoryAdd`,
	`"/v1/workspace/directories/remove" -> s.handleWorkspaceDirectoryRemove`,
	`"/v1/workspace/source-media/directories" -> s.handleWorkspaceSourceMediaDirectories`,
	`"/v1/workspace/source-media/directories/add" -> s.handleWorkspaceSourceMediaDirectoryAdd`,
	`"/v1/workspace/source-media/directories/remove" -> s.handleWorkspaceSourceMediaDirectoryRemove`,
	`"/v1/workspace/theme" -> s.handleWorkspaceTheme`,
	`"/v1/workspace/icon" -> s.handleWorkspaceIcon`,
	`"/v1/workspace/rename" -> s.handleWorkspaceRename`,
	`"/v1/workspace/move" -> s.handleWorkspaceMove`,
	`"/v1/workspace/todos" -> s.handleWorkspaceTodos`,
	`"/v1/workspace/actions" -> s.ownerOnlyScope(s.handleWorkspaceActions)`,
	`"/v1/workspace/skills/delete" -> s.handleWorkspaceSkillDelete`,
	`"/v1/workspace/actions/run" -> s.ownerOnlyScope(s.handleWorkspaceActionRunStart)`,
	`"/v1/workspace/actions/runs" -> s.ownerOnlyScope(s.handleWorkspaceActionRuns)`,
	`"/v1/workspace/actions/runs/cancel" -> s.ownerOnlyScope(s.handleWorkspaceActionRunCancel)`,
	`"/v1/workspace/git/status" -> s.handleGitStatus`,
	`"/v1/workspace/git/commit" -> s.handleGitCommit`,
	`"/v1/workspace/git/commit/suggestion" -> s.handleGitCommitSuggestion`,
	`"/v1/workspace/git/realtime" -> s.handleGitRealtime`,
	`"/v1/workspace/git/subscriptions" -> s.handleGitSubscriptions`,
	`"/v1/workspace/delete" -> s.handleWorkspaceDelete`,
	`"/v1/worktrees" -> s.handleWorktrees`,
	`"/v1/manage-worktree" -> s.handleManageWorktree`,
	`"/v1/connections" -> s.handleConnections`,
	`"/v1/task-environments" -> s.handleTaskEnvironments`,
	`"/v1/environments" -> s.handleEnvironments`,
	`"/v1/environments/" -> s.handleEnvironments`,
	`"/v1/deployments" -> s.handleDeployments`,
	`"/v1/context/sources" -> s.handleContextSources`,
	`"/v1/system/shutdown" -> s.handleSystemShutdown`,
	`"/v1/permissions" -> s.handlePermissions`,
	`"/v1/permissions/" -> s.handlePermissions`,
	`"/v1/alerts" -> s.handleNotifications`,
	`"/v1/alerts/" -> s.handleNotifications`,
	`"/v1/alerts/summary" -> s.handleNotifications`,
	`"/v1/alerts/inbox" -> s.handleNotifications`,
	`"/v1/notifications" -> s.handleNotifications`,
	`"/v1/notifications/" -> s.handleNotifications`,
	`"/v1/notifications/summary" -> s.handleNotifications`,
	`"/v1/notifications/inbox" -> s.handleNotifications`,
	`"/v1/storage/buckets" -> s.handleStorageBuckets`,
	`"/v1/storage/buckets/" -> s.handleStorageBuckets`,
	`"/v1/storage/canonical" -> s.handleStorageCanonical`,
	`"/v1/storage/proposals" -> s.handleStorageProposals`,
	`"/v1/storage/workers" -> s.handleStorageWorkers`,
	`"/v1/storage/workers/" -> s.handleStorageWorkers`,
	`"/v1/storage/deliverables" -> s.handleStorageDeliverables`,
	`"/v1/storage/deliverables/" -> s.handleStorageDeliverables`,
	`webPushRoutePrefix -> s.handleWebPush`,
	`webPushRoutePrefix + "/" -> s.handleWebPush`,
	`SessionStorageMaintenancePath -> s.handleSessionStorageMaintenance`,
	`SessionDumpPath -> s.handleSessionDump`,
	`sessionDumpFilePath -> s.handleSessionDumpFile`,
	`LongSessionDiagnosticsConfigPath -> s.handleLongSessionDiagnosticsConfig`,
	`LongSessionDiagnosticsSamplePath -> s.handleLongSessionDiagnosticsSample`,
	`LongSessionDiagnosticsCapturePath -> s.handleLongSessionDiagnosticsCapture`,
	`"/v1/integrations" -> s.handleIntegrations`,
	`"/v1/integrations/workspaces" -> s.handleIntegrationWorkspaces`,
	`"/v1/integrations/workspaces/" -> s.handleIntegrationWorkspaceByID`,
	`MediaStagingCollectionPath -> s.handleMediaStagingCollection`,
	`MediaStagingCollectionPath + "/" -> s.handleMediaStagingItem`,
	`RoutedSessionsPath -> s.handleRoutedSessionStart`,
	`BackgroundRouterSessionsPath -> s.handleBackgroundRouterSessionStart`,
	`WorkspaceOnboardingSessionsPath -> s.handleWorkspaceOnboardingSessionStart`,
	`V3RealtimeStreamPath -> s.handleV3RealtimeStream`,
	`V3SyncBootstrapPath -> s.handleSessionsV3SyncBootstrap`,
	`V3SyncHydratePath -> s.handleSessionsV3SyncHydrate`,
	`V3SyncStreamPath -> s.handleSessionsV3SyncStream`,
	`AutomationsV2Path -> s.handleAutomationsV2`,
	`AutomationsV2Path + "/proposal" -> s.handleAutomationsV2`,
	`AutomationsV2Path + "/review" -> s.handleAutomationsV2`,
	`AutomationsV2Path + "/accept" -> s.handleAutomationsV2`,
	`AutomationsV2Path + "/decline" -> s.handleAutomationsV2`,
	`AutomationsV2Path + "/control" -> s.handleAutomationsV2`,
	`AutomationsV2Path + "/progress" -> s.handleAutomationsV2`,
	`AutomationsV2Path + "/trigger" -> s.handleAutomationsV2`,
	`AutomationsV2Path + "/token" -> s.handleAutomationsV2`,
	`AutomationsV2Path + "/webhooks" -> s.handleAutomationsV2Webhooks`,
	`AutomationsV2Path + "/webhooks/" -> s.handleAutomationsV2Webhooks`,
	`DeliverablesPath -> s.handleDeliverables`,
	`DeliverablesPath + "/" -> s.handleDeliverables`,
	`WorkersEnsurePresetsPath -> s.handleWorkersEnsurePresets`,
	`ProjectsPath + ":order" -> s.handleProjects`,
	`"/v3/auth/tokens" -> s.handleAuthTokens`,
	`"/v3/auth/tokens/" -> s.handleAuthTokens`,
	`AutomationsPath -> s.handleAutomations`,
	`AutomationsPath + "/approve" -> s.handleAutomations`,
	`AutomationsPath + "/revoke" -> s.handleAutomations`,
	`"/v3/sessions:reconnect" -> s.handleSessionsV3Reconnect`,
	`"/v3/sessions:discover" -> s.handleSessionsV3Discovery`,
	`"/v3/sessions:search" -> s.handleSessionsV3Search`,
	`"/v3/sessions:usage" -> s.handleSessionsV3Usage`,
	`"/v3/usage" -> s.handleSessionsV3Usage`,
	`"/v3/usage/scope" -> s.handleUsageScope`,
	`"/v3/usage/scopes/repair" -> s.handleUsageScopeRepair`,
	`"/v3/sessions:usage-limits" -> s.handleSessionsV3UsageLimits`,
	`"/v3/sessions:end-user-usage" -> s.handleSessionsV3EndUserUsage`,
	`"/v3/limits" -> s.handleLimits`,
	`"/v3/permissions" -> s.handleAccountPermissions`,
	`"/v3/usage/limits" -> s.handleSessionsV3UsageLimits`,
	`"/v3/usage/worker-budget" -> s.handleWorkerBudget`,
	`"/v3/sessions:archive" -> s.handleSessionsV3Primary`,
	`"/v3/sessions:review-worktrees" -> s.handleSessionsV3ReviewWorktrees`,
	`"/v3/subagents:stop" -> s.handleSessionsV3SubagentStop`,
	`"/v3/sessions:unarchive" -> s.handleSessionsV3Unarchive`,
	`"/v3/sessions:delete" -> s.handleSessionsV3Delete`,
	`"/v3/sessions:agent" -> s.handleSessionsV3AgentSessions`,
	`"/v3/sessions:agent-events" -> s.handleSessionsV3AgentEvents`,
	`"/v3/artifacts" -> s.handleSessionsV3Artifacts`,
	`V3SyncLegacySessionsWorksetPath -> s.handleSessionsV3Workset`,
	`V3SyncLegacyTUIWorksetPath -> s.handleSessionsV3TUIWorkset`,
	`"/v3/tui/sessions" -> s.handleSessionsV3TUI`,
	`"/v3/tui/sessions/" -> s.handleSessionV3TUIByID`,
	`"/v3/application-agents" -> s.handleApplicationAgents`,
	`"/v3/application-agents/" -> s.handleApplicationAgents`,
	`"/v3/activity" -> s.handleActivity`,
	`"/v3/activity/stream" -> s.handleActivity`,
	`"/v3/sessions" -> s.handleSessionsV3Primary`,
	`"/v3/sessions/" -> s.handleSessionV3PrimaryByID`,
	`"/v1/sessions" -> s.handleSessions`,
	`"/v1/sessions/" -> s.handleSessionByID`,
}

// Purpose:
//   - Requirement: every route package api registers is either reached by an
//     MCP action over HTTP (and bound to the source the parity tests compare)
//     or listed as reached by none; a new route fails until it is placed.
//   - Threat/regression: a route added for an MCP action (or an MCP action
//     forwarded to a new route) with no parity entry, so the parity tests
//     silently skip it.
//   - Boundary/authority: the mux.Handle/HandleFunc registrations in the
//     package's source (server_routes.go and the files that register their
//     own routes).
//   - Narrowest layer: the registration source; no daemon runs.
func TestMCPRouteParityRegistrations(t *testing.T) {
	var drift []string
	registered := map[string]bool{}
	none := map[string]bool{}
	for _, key := range mcpRouteParityNoMCPAction {
		if none[key] {
			drift = append(drift, "mcpRouteParityNoMCPAction lists "+key+" twice")
		}
		none[key] = true
	}
	for _, r := range mcpRouteRegistrations(t) {
		key := r.Pattern + " -> " + r.Handler
		registered[key] = true
		_, reached := mcpRouteParityReached[key]
		switch {
		case reached && none[key]:
			drift = append(drift, key+" is in both mcpRouteParityReached and mcpRouteParityNoMCPAction")
		case !reached && !none[key]:
			drift = append(drift, key+" is a new route registration: list it in mcpRouteParityReached (with its mcpRouteSpecs entry) or mcpRouteParityNoMCPAction")
		}
	}
	for key := range mcpRouteParityReached {
		if !registered[key] {
			drift = append(drift, "mcpRouteParityReached lists "+key+", which is no longer registered: remove it")
		}
	}
	for key := range none {
		if !registered[key] {
			drift = append(drift, "mcpRouteParityNoMCPAction lists "+key+", which is no longer registered: remove it")
		}
	}
	if len(registered) < 100 {
		drift = append(drift, fmt.Sprintf("only %d route registrations found: the source scan is broken", len(registered)))
	}
	sort.Strings(drift)
	if len(drift) > 0 {
		t.Fatalf("route registration parity (%d):\n%s", len(drift), strings.Join(drift, "\n"))
	}
}
