import { buildStructuredToolMessage } from '../chat/services/tool-message'
import { ensureDesktopSession } from '../../../app/api'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createRootRoute, createRoute, createRouter, RouterProvider, Outlet } from '@tanstack/react-router'

// Purpose: deterministic data at the existing HTTP/cache/controller boundaries of
// OrchestratePage. No replacement page, conversation, composer, settings or media
// markup. This proves fixture UI contracts, NOT live provider/daemon execution,
// real reconnect durability, OS zoom, or physical/virtual keyboard behavior.
export type FixtureState = 'empty' | 'loading' | 'error' | 'populated'
export const sessionId = 'responsive-session-fixture'
export const taskTitle = `Review ${'long-unbroken-label-'.repeat(8)}`
export const project = { id: 'responsive-project', name: `Responsive ${'project-label-'.repeat(6)}`, description: 'Browser acceptance fixture', workspaces: [], primary_session_id: sessionId, project_context: '# Charter\n\n' + 'Long context '.repeat(40) }
export const task = { id: 'responsive-task', title: taskTitle, description: 'Inspect responsive presentation', tier: 'simple', status: 'running', agent: 'system-coder', session_id: sessionId, revision: 1, agents: [], created_at: 1, updated_at: 1 }
export const worker = { id: 'worker_responsive', account_scope_id: 'fixture-account', name: 'Responsive worker detail ' + 'worker-label-'.repeat(8), instructions: 'Inspect responsive UI', lifecycle_state: 'idle', revision: 1, created_at: 1, updated_at: 1, metadata: { project_id: project.id } }
export const media = { artifact_id: 'responsive-image', session_id: sessionId, collection_id: 'responsive-collection', variant_id: 'responsive-image', event_seq: 1, category: 'visual', kind: 'image', status: 'ready', label: 'Responsive image fixture', filename: 'responsive.png', media_type: 'image/png', updated_at: 1 }
// ChatMarkdown does not support semantic tables. Exercise its supported fenced
// table-shaped code representation; never inject a surrogate <table> renderer.
export const markdown = 'Responsive transcript\n\n```text\n' + 'unbroken-code-'.repeat(80) + '\n```\n\n```text\n| Column | Wide value |\n| --- | --- |\n| value | ' + 'table-value-'.repeat(60) + ' |\n```'
const bashOutput = Array.from({ length: 240 }, (_, n) => `${n}: ${'wide-output-'.repeat(20)}`).join('\n')
const messages = [
  { id: 'responsive-assistant', session_id: sessionId, global_seq: 1, role: 'assistant', content: markdown, created_at: 1 },
  ...['bash', 'read'].map((tool, index) => {
    const argumentsText = JSON.stringify(tool === 'bash' ? { command: 'printf bounded-output', explanation: 'Inspect fixture output', category: 'read', critical: false } : { path: 'src/fixture.ts' })
    const outputText = tool === 'bash' ? bashOutput : JSON.stringify({ path: 'src/fixture.ts', lines: [{ line: 1, text: 'wide-read-'.repeat(80) }], line_start: 1, total_lines: 1 })
    return { id: `responsive-${tool}`, session_id: sessionId, global_seq: index + 2, role: 'tool', created_at: index + 2,
      toolMessage: buildStructuredToolMessage({ tool, callId: `responsive-call-${tool}`, argumentsText, outputText, state: 'done' }),
      // Canonical cache hydration parses this wire payload through the same builder.
      content: JSON.stringify({ path_id: 'run.tool-history.v2', tool, call_id: `responsive-call-${tool}`, arguments: argumentsText, output: outputText }) }
  }),
]
export function snapshot(state: FixtureState = 'populated') {
  const items = state === 'empty' ? [] : messages
  return { ok: true as const, rev: 1, scope_id: 'responsive-scope', snapshot_endpoint_cursor: 'opaque-responsive-cursor', session_order: [sessionId],
    sessions_by_id: { [sessionId]: { id: sessionId, workspace_path: '.', workspace_name: 'fixture', title: 'Responsive conversation', mode: 'auto', created_at: 1, updated_at: 3, message_count: items.length, last_message_at: items.length ? 3 : 0 } },
    projections_by_session: { [sessionId]: { session_id: sessionId, last_event_seq: 3, projection_high_watermark_seq: 3, updated_at: 3 } },
    messages_by_session: { [sessionId]: items }, events_by_session: { [sessionId]: [] }, session_views_by_id: { [sessionId]: { pending_permissions: [], has_active_plan: false, active_plan: null } },
    permission_summaries_by_session: { [sessionId]: { session_id: sessionId, pending_approval_count: 0 } },
    selector: { kind: 'session_ids', session_ids: [sessionId] }, known_sessions: {}, tombstones_by_session: {}, sync_scope: { surface: 'desktop', stream_kind: 'v3.sync.snapshot', selector_filter_hash: 'fixture', resource_set: 'messages,events,run_intents,active_plan,permission_summary' },
    replay_instructions: { stream_path: '/v3/sync/stream', transport: 'http_post', after_endpoint_cursor: 'opaque-responsive-cursor', bootstrap_required_on_cursor_error: true },
  }
}

// Explicit finite reads. Unknown endpoints fail, rather than silently claiming a
// successful contract with {}. Such failures are recorded by the browser runner.
export function fixtureRead(url: URL, state: FixtureState): unknown | undefined {
  const p = url.pathname
  if (p === '/v1/auth/desktop/session') return { ok: true, user_id: 'fixture-operator', account_scope_id: 'fixture-account', username: 'Operator' }
  if (p === '/v1/me') return { userID: 'fixture-operator', username: 'Operator' }
  if (p === '/v1/workspace/list') return { workspaces: [] }
  if (p === '/v1/workspace/discover') return { directories: [] }
  if (p === '/v1/workspace/browse') return { browser: { requested_path: '.', resolved_path: '.', home_path: '.', root_path: '.', entries: [] } }
  if (p === '/v1/workspace/overview') return { workspaces: [], discovered: [], has_more: false, next_cursor: 0 }
  if (p === '/v1/model-profiles') return { model_profiles: [], default_profile_id: '' }
  if (p === '/v1/model') return { preference: { provider: '', model: '', thinking: '' }, context_window: 0, max_output_tokens: 0 }
  if (p === '/v2/agents') return { state: { profiles: [], active_name: '' } }
  if (p === '/v1/notifications/push') return { status: { enabled: false, public_key: '', subscription_count: 0 } }
  if (p === '/v1/workspace/source-media/directories') return { ok: true, source_media_directories: [] }
  if (p === '/v3/projects') return { projects: state === 'empty' ? [] : [project] }
  if (p === `/v3/projects/${project.id}`) return { project }
  if (p === `/v3/projects/${project.id}/tasks`) return { tasks: state === 'empty' || url.searchParams.has('archived') ? [] : [task] }
  if (p === `/v3/projects/${project.id}/tasks/${task.id}`) return { task }
  if (p === `/v3/projects/${project.id}/tasks/${task.id}/history`) return { attempts: [{ id: 'responsive-history', session_id: sessionId, role: 'execution', created_at: 1, status: 'completed', request: 'Historical responsive request ' + 'history-label-'.repeat(60), summary: 'Retained historical summary ' + 'summary-'.repeat(40) }], next_cursor: 0 }
  if (p === `/v3/projects/${project.id}/media`) return { media: [] }
  if (p === '/v1/ui/settings') return { theme: { custom_themes: [{ id: 'fixture-theme', name: 'Fixture theme', palette: {} }] }, swarm: {}, permissions: {}, notifications: {} }
  // Explicit empty catalogs at the current production read boundaries; unknown
  // routes still fail. The transcript fixture above declares its resource set.
  if (p === `/v3/sessions/${sessionId}/repositories`) return { repositories: [] }
  if (p === `/v3/sessions/${sessionId}/artifacts-v3`) return { artifacts: [] }
  if (p === `/v3/sessions/${sessionId}/artifact-v2`) return { collections: [] }
  if (p === `/v3/projects/${project.id}/designs`) return { requests: [], next_cursor: '' }
  if (p === '/v3/usage/scope') return { recorded: false }
  if (p === '/v3/usage/worker-budget') return { recorded: false }
  if (p === '/v1/agent-model-settings') {
    const assignment = { provider: 'fixture', model: 'fixture', thinking: 'low' }
    return { agent_model_settings: { swarm: { action: assignment, plan: assignment }, system_agents: Object.fromEntries(['compact', 'finder', 'coder', 'designer', 'router'].map(key => [key, assignment])), updated_at: 1 } }
  }
  if (p === '/v1/media/settings/catalog') return { image_models: [], video_generation_models: [], audio_models: [], video_ready: false, video_status: 'Fixture media generation unconfigured', audio_ready: false, audio_status: 'Fixture media generation unconfigured' }
  if (p === '/v1/providers') return { providers: [] }
  if (p === '/v1/auth/credentials') return { credentials: [] }
  if (p === '/v3/auth/tokens') return { ok: true, tokens: [] }
  if (p === '/v3/workers') return { workers: state === 'empty' || url.searchParams.get('lifecycle_state') === 'pending' ? [] : [worker] }
  if (p === `/v3/workers/${worker.id}`) return { worker }
  if (p === `/v3/workers/${worker.id}/runs`) return { runs: [] }
  if (p === `/v3/workers/${worker.id}/history`) return { revisions: [] }
  if (p === `/v3/workers/${worker.id}/summary`) return { worker_id: worker.id, next_scheduled_at: 0, runs: { active: [], active_runs: 0, active_truncated: false, date: url.searchParams.get('date'), timezone: url.searchParams.get('timezone'), daily_runs: 0, daily_success: 0, daily_failed: 0, daily_cancelled: 0, scanned_runs: 0, truncated: false, day_start_at: 0, day_end_at: 0 } }
  if (p === '/v3/artifacts') return { ok: true, artifacts: state === 'empty' ? [] : [media] }
  if (p === '/v1/vault') return { enabled: false, unlocked: true, unlock_required: false, storage_mode: 'memory' }
  if (p === '/v1/permissions') return { ok: true, policy: { version: 1, rules: [], bash_profile: 'current_rules' }, bypass_permissions: false }
  if (p === '/v1/permissions/capabilities') return { session_deploy: { mode: 'ask', automatic_deployments_per_parent_run: 0, over_limit_action: 'ask' }, plan_acceptance: { mode: 'ask' }, active_execution_limit: 100 }
  if (p === `/v3/sessions/${sessionId}/media-capability`) return { media_capability: { status: 'available', contract_version: 1, contract_token: 'fixture-only', capabilities: [{ modality: 'image', mime_types: ['image/png'], max_bytes: 1024, max_count: 1 }] } }
  return undefined
}

export async function mountResponsiveFixture(state: FixtureState) {
  await ensureDesktopSession()
  const { OrchestratePage } = await import('./orchestrate-page')
  const { dispatchDesktopV3Cache } = await import('../state/desktop-v3-cache-store')
  const { hydrateResponseToAction } = await import('../state/desktop-v3-cache-wire')
  const { retainDesktopV3RealtimeController, setDesktopV3RealtimeControllerFactoryForTests } = await import('../realtime/v3-realtime-controller')
  const { modelOptionsQueryOptions } = await import('../../queries/query-options')
  const { agentModelSettingsQueryKey } = await import('../settings/swarm/queries/get-agent-model-settings')
  const win = window as any
  win.responsive = { acquired: [], released: [], connected: [] }
  setDesktopV3RealtimeControllerFactoryForTests(() => ({
    start: async () => {}, stop: () => {}, diagnostics: () => ({}),
    ensureSessionConnected: async (id: string) => { win.responsive.connected.push(id) },
    acquireSessionDemand: (ownerKey: string, id: string) => {
      win.responsive.acquired.push([ownerKey, id])
      let released = false
      return { ownerKey, sessionId: id, ready: Promise.resolve(), release: () => { if (!released) win.responsive.released.push([ownerKey, id]); released = true } }
    },
  }) as any)
  const lease = retainDesktopV3RealtimeController({ ownerKey: 'responsive-fixture' })
  if (state === 'populated' || state === 'empty') dispatchDesktopV3Cache(hydrateResponseToAction(snapshot(state), [sessionId]))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity, refetchOnWindowFocus: false } } })
  client.setQueryData(modelOptionsQueryOptions().queryKey, [])
  const assignment = { provider: '', model: '', thinking: '', serviceTier: '', contextMode: '' }
  client.setQueryData(agentModelSettingsQueryKey, { roles: [{ id: 'system-orchestrator', label: 'Swarm Orchestrator', group: 'swarm', slot: 'plan' }, { id: 'system-coder', label: 'Coder', group: 'system_agents', slot: 'coder' }], swarm: { action: assignment, plan: assignment }, systemAgents: Object.fromEntries(['compact', 'finder', 'coder', 'designer', 'router'].map(key => [key, assignment])), updatedAt: 1 })
  const rootRoute = createRootRoute({ component: Outlet })
  const layout = createRoute({ getParentRoute: () => rootRoute, id: 'swarm-layout', component: () => <OrchestratePage workspaceSlug="fixture" />, validateSearch: (s: Record<string, unknown>) => ({ workerId: typeof s.workerId === 'string' ? s.workerId : undefined }) })
  const home = createRoute({ getParentRoute: () => layout, path: '/$workspaceSlug/swarm' })
  const section = createRoute({ getParentRoute: () => layout, path: '/$workspaceSlug/swarm/$swarmSection' })
  const router = createRouter({ routeTree: rootRoute.addChildren([layout.addChildren([home, section])]) })
  win.responsive.running = (active: boolean) => {
    const data = snapshot()
    const run = { session_id: sessionId, run_id: 'responsive-run', status: active ? 'running' : 'completed', created_at: 1, updated_at: 5, event_seq: 5 }
    dispatchDesktopV3Cache(hydrateResponseToAction({ ...data, run_intents_by_session: { [sessionId]: [run] }, current_run_state_by_session: { [sessionId]: { ...run, active } } }, [sessionId]))
  }
  win.responsive.navigate = (section: string, search = {}) => router.navigate({ to: section ? '/$workspaceSlug/swarm/$swarmSection' : '/$workspaceSlug/swarm', params: { workspaceSlug: 'fixture', swarmSection: section }, search } as any)
  const root = createRoot(document.getElementById('root')!)
  win.responsive.unmount = () => { root.unmount(); lease.release(); client.clear() }
  root.render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>)
}
