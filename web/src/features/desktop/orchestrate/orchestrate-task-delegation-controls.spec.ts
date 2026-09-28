import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  buildTaskAcceptancePayload,
  getPrimarySystemAgentName,
  isMediaTaskType,
  resolveDeployImpendingConfig,
  resolveOptimisticApprovedDeliverables,
  resolveTaskImpendingAgents,
  resolveTaskWorkspace,
  taskWorkspaceSelection,
  taskDeployRequestIdentity,
} from './orchestrate-task-helpers'
import { mapBackendTask } from '../state/desktop-projects-state'
import type { RunningTask, BackendTaskModelPreview } from './orchestrate-types'
import type { AgentModelSettings } from '../settings/swarm/types/agent-model-settings'
import type { AgentModelControlTaskOverrideInput } from '../chat/components/agent-model-control'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)

const mockAgentModelSettings: AgentModelSettings = {
  swarm: {
    action: {
      provider: 'google',
      model: 'gemini-2.5-pro',
      thinking: 'medium',
      serviceTier: 'standard',
      contextMode: '',
    },
    plan: {
      provider: 'openai',
      model: 'gpt-6-astra-plan',
      thinking: 'high',
      serviceTier: 'priority',
      contextMode: '',
    },
  },
  systemAgents: {
    coder: {
      provider: 'anthropic',
      model: 'claude-3-7-sonnet',
      thinking: 'high',
      serviceTier: 'standard',
      contextMode: '',
    },
    finder: {
      provider: 'google',
      model: 'gemini-2.5-flash',
      thinking: 'low',
      serviceTier: 'standard',
      contextMode: '',
    },
    designer: {
      provider: 'openai',
      model: 'gpt-6-astra',
      thinking: 'medium',
      serviceTier: 'standard',
      contextMode: '',
    },
    compact: {
      provider: 'google',
      model: 'gemini-2.5-flash',
      thinking: 'off',
      serviceTier: 'standard',
      contextMode: '',
    },
    router: {
      provider: 'google',
      model: 'gemini-2.5-flash',
      thinking: 'off',
      serviceTier: 'standard',
      contextMode: '',
    },
  },
  updatedAt: 1790000000000,
}

test('Explicit New Task delegation: Small feature vs Big feature vs Audit without prompt heuristics', () => {
  // Requirement: User wants New Task explicit Small feature/fix vs Big feature:
  // - small -> Coder, outcome code_pr, tier direct
  // - big -> plan-mode orchestration visible proposed plan awaiting approval, outcome plan_spec, tier complex
  // - audit -> Finder, outcome audit_report, tier discovery
  // - Mentioning media words (PNG, image, screen record) in prompt MUST NOT hijack code engineering into media generation.

  // Case 1: Small feature / fix with media keywords in prompt text
  const smallPrompt = 'Allow profile PNG upload and selection from media shelf in user profile settings'
  const smallConfig = resolveDeployImpendingConfig('code', 'small', '', mockAgentModelSettings)
  assert.equal(smallConfig.targetAgent, 'coder', 'Small feature must target Coder agent')
  assert.equal(smallConfig.targetOutcomeType, 'code_pr', 'Small feature must target code_pr outcome')
  assert.equal(smallConfig.targetTier, 'direct', 'Small feature must use direct execution tier')
  assert.equal(smallConfig.resolvedModel, 'claude-3-7-sonnet', 'Small feature must use account Coder default model')
  assert.equal(smallConfig.isOverridden, false)

  // Case 2: Big feature / complex overhaul
  const bigConfig = resolveDeployImpendingConfig('code', 'big', '', mockAgentModelSettings)
  assert.equal(bigConfig.targetAgent, 'plan', 'Big feature must target Plan agent')
  assert.equal(bigConfig.targetOutcomeType, 'plan_spec', 'Big feature must target plan_spec outcome')
  assert.equal(bigConfig.targetTier, 'complex', 'Big feature must use complex execution tier')
  assert.equal(bigConfig.resolvedModel, 'gpt-6-astra-plan', 'Big feature must use account Swarm plan model')
  assert.equal(bigConfig.isOverridden, false)

  // Case 3: Audit exploration
  const auditConfig = resolveDeployImpendingConfig('audit', 'small', '', mockAgentModelSettings)
  assert.equal(auditConfig.targetAgent, 'finder', 'Audit must target Finder agent')
  assert.equal(auditConfig.targetOutcomeType, 'audit_report', 'Audit must target audit_report outcome')
  assert.equal(auditConfig.targetTier, 'discovery', 'Audit must use discovery execution tier')
  assert.equal(auditConfig.resolvedModel, 'gemini-2.5-flash', 'Audit must use account Finder default model')
})

test('Task Impending Agents and Resolved Models: grouped Coders and single agents', () => {
  // Requirement: Show actual impending agent(s) and resolved models on pending task cards and modal,
  // including grouped Coders for parallel task programs.

  // Case 1: Task program with 3 jobs (2 Coders + 1 Finder)
  const taskProgramTask: Pick<RunningTask, 'agentType' | 'model' | 'outcomeType'> = {
    agentType: 'coder',
    model: '',
    outcomeType: 'code_pr',
  }
  const programJobs = [
    { id: 'job-1', agent_type: 'coder' },
    { id: 'job-2', agent_type: 'coder' },
    { id: 'job-3', agent_type: 'finder' },
  ]

  const impendingCohort = resolveTaskImpendingAgents(
    taskProgramTask,
    programJobs,
    undefined,
    mockAgentModelSettings
  )
  assert.equal(impendingCohort.length, 2, 'Should group cohort by agent type')

  const coderCohort = impendingCohort.find((c) => c.agent === 'coder')
  assert.ok(coderCohort, 'Must have grouped coder cohort')
  assert.equal(coderCohort.count, 2, 'Should group 2 coders together')
  assert.equal(coderCohort.label, '2 Coders (@coder)')
  assert.equal(coderCohort.model, 'claude-3-7-sonnet', 'Should resolve account Coder model')
  assert.equal(coderCohort.isOverride, false)

  const finderCohort = impendingCohort.find((c) => c.agent === 'finder')
  assert.ok(finderCohort, 'Must have finder entry')
  assert.equal(finderCohort.count, 1)
  assert.equal(finderCohort.model, 'gemini-2.5-flash')

  // Case 2: Single task with task-level model override
  const overriddenTask: Pick<RunningTask, 'agentType' | 'model' | 'outcomeType'> = {
    agentType: 'coder',
    model: 'custom-fine-tuned-model',
    outcomeType: 'code_pr',
  }
  const singleCohort = resolveTaskImpendingAgents(
    overriddenTask,
    [],
    undefined,
    mockAgentModelSettings
  )
  assert.equal(singleCohort.length, 1)
  assert.equal(singleCohort[0].agent, 'coder')
  assert.equal(singleCohort[0].model, 'custom-fine-tuned-model')
  assert.equal(singleCohort[0].isOverride, true, 'Must indicate task-level override')
})

test('Pending tasks can change models before deploy; updates dynamic defaults and task overrides', () => {
  // Requirement: Pending tasks can change models before deploy.
  // Reused /agents authority maps to canonical agent IDs without duplicating UI.

  assert.equal(getPrimarySystemAgentName('coder'), 'system-coder')
  assert.equal(getPrimarySystemAgentName('finder'), 'system-finder')
  assert.equal(getPrimarySystemAgentName('plan'), 'swarm')
  assert.equal(getPrimarySystemAgentName('swarm'), 'swarm')
  assert.equal(getPrimarySystemAgentName('image'), 'system-designer')

  // Deploy config with task-only override
  const overriddenConfig = resolveDeployImpendingConfig(
    'code',
    'small',
    'specialized-coder-override',
    mockAgentModelSettings
  )
  assert.equal(overriddenConfig.resolvedModel, 'specialized-coder-override')
  assert.equal(overriddenConfig.isOverridden, true)
  assert.equal(overriddenConfig.accountDefaultModel, 'claude-3-7-sonnet')

  // Updated account default settings dynamically updates the resolved model
  const updatedSettings: AgentModelSettings = {
    ...mockAgentModelSettings,
    systemAgents: {
      ...mockAgentModelSettings.systemAgents,
      coder: {
        ...mockAgentModelSettings.systemAgents.coder,
        model: 'claude-3-5-sonnet-latest',
      },
    },
  }
  const updatedDefaultConfig = resolveDeployImpendingConfig(
    'code',
    'small',
    '',
    updatedSettings
  )
  assert.equal(
    updatedDefaultConfig.resolvedModel,
    'claude-3-5-sonnet-latest',
    'Should reflect updated account settings default'
  )
  assert.equal(updatedDefaultConfig.isOverridden, false)
})

test('Approval optimistic placeholder never fabricates image deliverables for code tasks', () => {
  // Requirement: approval optimistic placeholder currently fabricates image deliverables for code at ~3261—remove misleading display.

  // Case 1: Code task with empty deliverables
  const codeTask: Pick<RunningTask, 'id' | 'title' | 'agentType' | 'outcomeType' | 'variantCount' | 'workerName' | 'deliverables'> = {
    id: 'task-123',
    title: 'Implement OAuth authentication',
    agentType: 'coder',
    outcomeType: 'code_pr',
    variantCount: 1,
    workerName: '@coder Worker',
    deliverables: [],
  }
  const codeDeliverables = resolveOptimisticApprovedDeliverables(codeTask)
  assert.equal(
    codeDeliverables.length,
    0,
    'Code tasks must NOT receive fabricated image/video variant deliverables'
  )

  // Case 2: Media image task with empty deliverables
  const mediaTask: Pick<RunningTask, 'id' | 'title' | 'agentType' | 'outcomeType' | 'variantCount' | 'workerName' | 'deliverables'> = {
    id: 'task-456',
    title: 'Neon cyberpunk logo',
    agentType: 'image',
    outcomeType: 'media_bundle',
    variantCount: 3,
    workerName: '@image Worker',
    deliverables: [],
  }
  const mediaDeliverables = resolveOptimisticApprovedDeliverables(mediaTask)
  assert.equal(
    mediaDeliverables.length,
    3,
    'Media tasks should generate optimistic variant slot placeholders'
  )
  assert.equal(mediaDeliverables[0].type, 'image')
  assert.equal(mediaDeliverables[0].status, 'generating')
})

test('OrchestrateView UI wiring contract: tabs, model preview, change triggers, and /agents authority', () => {
  // Invariants in OrchestrateView.tsx:
  // - 6 explicit tabs: Small Feature/Fix, Big Feature, Image, Video, Sounds, Audit
  // - Pending proposal details expanded by default (isFullPlanOpen = isPendingApproval)
  // - Reused AgentModelControl mounted with setupOpenSignal and initialAgentName
  // - Impending execution banner with data-testid attributes
  // - Task card model changer with apply override and reset buttons
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // Category tabs testids
  assert.ok(
    source.includes('data-testid="deploy-tab-small-feature"'),
    'Must render Small Feature/Fix tab'
  )
  assert.ok(
    source.includes('data-testid="deploy-tab-big-feature"'),
    'Must render Big Feature tab'
  )
  assert.ok(
    source.includes('data-testid="deploy-tab-audit"'),
    'Must render Audit tab'
  )

  // Deploy modal impending banner & model selection
  assert.ok(
    source.includes('data-testid="deploy-modal-impending-preview"'),
    'Must render impending agent & model preview banner in deploy modal'
  )
  assert.ok(
    source.includes('data-testid="deploy-modal-change-model-btn"'),
    'Must render change model button in deploy modal'
  )
  assert.ok(
    source.includes('data-testid="modal-open-agents-btn"'),
    'Must render Configure Default in /agents button in deploy modal'
  )

  // Pending task card impending agents & model changer
  assert.ok(
    source.includes('data-testid="task-impending-agents"'),
    'Must render impending execution agents on pending task cards'
  )
  assert.ok(
    source.includes('data-testid="task-card-change-model-btn"'),
    'Must render change model button on pending task cards'
  )
  assert.ok(
    source.includes('data-testid="task-model-apply-override-btn"'),
    'Must render apply override button in task model changer'
  )
  assert.ok(
    source.includes('data-testid="task-model-reset-default-btn"'),
    'Must render reset to default button in task model changer'
  )
  assert.ok(
    source.includes('data-testid="task-model-open-agents-btn"'),
    'Must render open /agents button in task card model changer'
  )

  // Expanded by default
  assert.ok(
    source.includes('useState(isPendingApproval)'),
    'Pending approval proposals must have full plan details expanded by default'
  )

  // Reused AgentModelControl mount
  assert.ok(
    source.includes('<AgentModelControl'),
    'Must mount reused AgentModelControl component'
  )
  assert.ok(
    source.includes('setupOpenSignal={agentSettingsOpenSignal}'),
    'Must wire setupOpenSignal to AgentModelControl'
  )
  assert.ok(
    source.includes('initialAgentName={agentSettingsInitialAgent}'),
    'Must wire initialAgentName to AgentModelControl'
  )

  // Reconciled POST body with feature_size and model
  assert.ok(
    source.includes('feature_size: taskIntent === \'code\' ? featureSize : undefined'),
    'Must pass feature_size in POST /v3/projects/{id}/tasks'
  )
  assert.ok(
    source.includes('agent: targetAgent'),
    'Must pass explicit agent in POST /v3/projects/{id}/tasks'
  )

  // No hardcoded model fallbacks
  assert.doesNotMatch(
    source,
    /'veo-3.1-generate-preview'/,
    'Hardcoded veo-3.1 model fallback must be removed'
  )

  // No duplicate model-only dropdown in deploy modal
  assert.doesNotMatch(
    source,
    /data-testid="deploy-modal-model-select"/,
    'Duplicate deploy-modal-model-select dropdown must be removed in favor of AgentModelControl'
  )

  // Reused AgentModelControl receives task-scoped props and handlers
  assert.ok(
    source.includes('taskScoped={agentSettingsTaskContext !== null}'),
    'Must pass taskScoped prop to reused AgentModelControl'
  )
  assert.ok(
    source.includes('onApplyTaskModel={handleApplyTaskModelFromControl}'),
    'Must wire onApplyTaskModel handler to reused AgentModelControl'
  )
  assert.ok(
    source.includes('onResetTaskModel={handleResetTaskModelFromControl}'),
    'Must wire onResetTaskModel handler to reused AgentModelControl'
  )

  // Request-driven backend preview queries without polling
  assert.ok(
    source.includes("tasks:preview'"),
    'Must query POST /v3/projects/{id}/tasks:preview for authoritative deploy model preview'
  )
  assert.ok(
    source.includes("model-preview'"),
    'Must query GET /v3/projects/{id}/tasks/{taskId}/model-preview for authoritative task model preview'
  )
  assert.ok(
    source.includes('data-testid="deploy-modal-preview-error"'),
    'Must render deploy modal preview error banner when backend preview fails'
  )
  assert.ok(
    source.includes('data-testid="task-model-preview-error"'),
    'Must render task card preview error banner when backend preview fails'
  )
})

test('Authoritative Backend Model Preview consumption and visible error handling', () => {
  // Requirement: UI must consume backend actual resolved agents/model/error
  // and avoid showing wrong provider/defaults.
  // Missing preview fails visibly, not a guessed label.

  // Case 1: Backend returns authoritative resolved model with full metadata
  const task: Pick<RunningTask, 'agentType' | 'model' | 'outcomeType'> = {
    agentType: 'coder',
    model: 'claude-3-7-sonnet',
    outcomeType: 'code_pr',
  }
  const backendPreview: BackendTaskModelPreview = {
    task_id: 'task-100',
    agent: 'coder',
    resolved_agent: 'coder',
    feature_size: 'small',
    task_model_override: 'claude-3-7-sonnet',
    resolved_model: {
      provider: 'anthropic',
      model: 'claude-3-7-sonnet',
      thinking: 'high',
      service_tier: 'standard',
      context_mode: '',
    },
    model_source: 'task_override',
    account_default_model: {
      provider: 'anthropic',
      model: 'claude-3-5-sonnet-latest',
      thinking: 'medium',
      service_tier: 'standard',
    },
    account_settings_path: '/v3/agents/model-settings',
  }

  const cohorts = resolveTaskImpendingAgents(
    task,
    [],
    undefined,
    mockAgentModelSettings,
    undefined,
    backendPreview
  )
  assert.equal(cohorts.length, 1)
  assert.equal(cohorts[0].model, 'claude-3-7-sonnet', 'Must use backend resolved model')
  assert.equal(cohorts[0].provider, 'anthropic', 'Must preserve provider from backend')
  assert.equal(cohorts[0].thinking, 'high', 'Must preserve thinking level from backend')
  assert.equal(cohorts[0].isOverride, true, 'Must reflect task_override source')
  assert.equal(cohorts[0].previewFailed, undefined)

  // Case 2: Backend preview fails with an error -> FAILS VISIBLY without guessing label!
  const failedCohorts = resolveTaskImpendingAgents(
    task,
    [],
    undefined,
    mockAgentModelSettings,
    undefined,
    null,
    'Failed to connect to daemon model preview endpoint'
  )
  assert.equal(failedCohorts.length, 1)
  assert.equal(failedCohorts[0].previewFailed, true, 'Must be marked as previewFailed')
  assert.equal(failedCohorts[0].model, 'Preview unavailable', 'Must visibly display Preview unavailable')
  assert.equal(failedCohorts[0].error, 'Failed to connect to daemon model preview endpoint')

  // Case 3: Deploy impending config with backend preview
  const deployConfig = resolveDeployImpendingConfig(
    'code',
    'small',
    '',
    mockAgentModelSettings,
    undefined,
    backendPreview
  )
  assert.equal(deployConfig.resolvedModel, 'claude-3-7-sonnet')
  assert.equal(deployConfig.provider, 'anthropic')
  assert.equal(deployConfig.thinking, 'high')
  assert.equal(deployConfig.isOverridden, true)
  assert.equal(deployConfig.accountDefaultModel, 'claude-3-5-sonnet-latest')

  // Case 4: Deploy impending config with preview error
  const failedDeployConfig = resolveDeployImpendingConfig(
    'code',
    'small',
    '',
    mockAgentModelSettings,
    undefined,
    null,
    'Backend unavailable'
  )
  assert.equal(failedDeployConfig.previewFailed, true)
  assert.equal(failedDeployConfig.resolvedModel, 'Preview unavailable')
  assert.equal(failedDeployConfig.error, 'Backend unavailable')
})

test('Grouped Coders cohort with backend authoritative preview and task-level override', () => {
  // Requirement: Task programs with multiple coders must group correctly,
  // showing count and label (e.g. "2 Coders (@coder)"), and consume backend-resolved model.

  const multiCoderTask: Pick<RunningTask, 'agentType' | 'model' | 'outcomeType'> = {
    agentType: 'coder',
    model: 'gemini-2.5-pro-override',
    outcomeType: 'code_pr',
  }
  const programJobs = [
    { id: 'job-1', agent_type: 'coder' },
    { id: 'job-2', agent_type: 'coder' },
    { id: 'job-3', agent_type: 'finder' },
  ]
  const backendPreview: BackendTaskModelPreview = {
    task_id: 'task-prog-1',
    agent: 'coder',
    resolved_agent: 'coder',
    task_model_override: 'gemini-2.5-pro-override',
    resolved_model: {
      provider: 'google',
      model: 'gemini-2.5-pro-override',
      thinking: 'high',
      service_tier: 'priority',
      context_mode: '',
    },
    model_source: 'task_override',
    account_settings_path: '/v3/agents/model-settings',
  }

  const cohorts = resolveTaskImpendingAgents(
    multiCoderTask,
    programJobs,
    undefined,
    mockAgentModelSettings,
    undefined,
    backendPreview
  )
  assert.equal(cohorts.length, 2, 'Should group into Coder and Finder cohorts')

  const coderCohort = cohorts.find((c) => c.agent === 'coder')!
  assert.ok(coderCohort)
  assert.equal(coderCohort.count, 2)
  assert.equal(coderCohort.label, '2 Coders (@coder)')
  assert.equal(coderCohort.model, 'gemini-2.5-pro-override')
  assert.equal(coderCohort.provider, 'google')
  assert.equal(coderCohort.thinking, 'high')
  assert.equal(coderCohort.serviceTier, 'priority')
  assert.equal(coderCohort.isOverride, true)

  const finderCohort = cohorts.find((c) => c.agent === 'finder')!
  assert.ok(finderCohort)
  assert.equal(finderCohort.count, 1)
  assert.equal(finderCohort.model, 'gemini-2.5-flash')
  assert.equal(finderCohort.provider, 'google')
  assert.equal(finderCohort.isOverride, false)

  // Requirement: a Coder-only task override must never leak into a Finder job
  // when the backend preview is absent; each cohort retains its own authority.
  const withoutPreview = resolveTaskImpendingAgents(
    multiCoderTask, programJobs, undefined, mockAgentModelSettings
  )
  assert.equal(withoutPreview.find((c) => c.agent === 'coder')?.model, 'gemini-2.5-pro-override')
  assert.equal(withoutPreview.find((c) => c.agent === 'coder')?.isOverride, true)
  assert.equal(withoutPreview.find((c) => c.agent === 'finder')?.model, 'gemini-2.5-flash')
  assert.equal(withoutPreview.find((c) => c.agent === 'finder')?.isOverride, false)
})

test('AgentModelControl contract: supports task-scoped apply, save-default, and reset override', () => {
  // Requirement: Reuse /agents UX, no duplicate UI.
  // AgentModelControl supports task-scoped apply and save-default as needed,
  // maintaining canonical settings authority.
  const controlPath = path.join(__dirname, '../chat/components/agent-model-control.tsx')
  const controlSource = fs.readFileSync(controlPath, 'utf8')

  // Verify task-scoped types and props
  assert.ok(
    controlSource.includes('export type AgentModelControlTaskOverrideInput'),
    'Must export AgentModelControlTaskOverrideInput'
  )
  assert.ok(
    controlSource.includes('taskScoped?: boolean'),
    'Must declare taskScoped prop'
  )
  assert.ok(
    controlSource.includes('onApplyTaskModel?: (input: AgentModelControlTaskOverrideInput) => void | Promise<void>'),
    'Must declare onApplyTaskModel prop'
  )
  assert.ok(
    controlSource.includes('onResetTaskModel?: () => void | Promise<void>'),
    'Must declare onResetTaskModel prop'
  )

  // Verify action buttons in task-scoped mode
  assert.ok(
    controlSource.includes('data-testid="task-model-apply-override-btn"'),
    'Must render Apply to Task button with data-testid="task-model-apply-override-btn"'
  )
  assert.ok(
    controlSource.includes('data-testid="task-model-reset-default-btn"'),
    'Must render Reset Task Override button with data-testid="task-model-reset-default-btn"'
  )
  assert.ok(
    controlSource.includes('data-testid="task-model-open-agents-btn"'),
    'Must render Save as Default button with data-testid="task-model-open-agents-btn"'
  )

  // Verify canonical settings authority is preserved
  assert.ok(
    controlSource.includes('saveSystemAgentModelSettings'),
    'Must save system agent model defaults via canonical settings authority'
  )
  assert.ok(
    controlSource.includes('saveSwarmModels'),
    'Must save Swarm action/plan model defaults via canonical authority'
  )
})

test('Task acceptance payload and revision guard contract', () => {
  // Requirement: Task acceptance must supply exact backend revision guards { session_id, plan_id, definition_revision }.
  // Only exact backend plan binding definition_revision is authoritative.
  // Missing binding must remain missing and block plan acceptance.
  // Prefer binding.session_id for plan guard rather than unrelated execution session.

  // Case 1: Big feature task with normalized camelCase planBinding
  const camelTask = {
    sessionId: 'sess-exec-001',
    planBinding: {
      planId: 'plan-auth-v1',
      definitionRevision: 3,
      sessionId: 'sess-big-001',
    },
  }
  const payload1 = buildTaskAcceptancePayload(camelTask)
  assert.equal(payload1.session_id, 'sess-big-001', 'Must prefer binding.sessionId over task.sessionId for plan guard')
  assert.equal(payload1.plan_id, 'plan-auth-v1')
  assert.equal(payload1.definition_revision, 3)

  // Case 2: Backend raw task with snake_case plan_binding
  const snakeTask = {
    session_id: 'sess-exec-002',
    plan_binding: {
      plan_id: 'plan-auth-v2',
      definition_revision: 5,
      session_id: 'sess-big-002',
    },
  }
  const payload2 = buildTaskAcceptancePayload(snakeTask)
  assert.equal(payload2.session_id, 'sess-big-002', 'Must prefer binding.session_id over task.session_id for plan guard')
  assert.equal(payload2.plan_id, 'plan-auth-v2')
  assert.equal(payload2.definition_revision, 5)

  // Case 3 (Negative test): Missing plan binding must NOT fall back to planDocument version or task revision
  const noBindingTask = {
    sessionId: 'sess-plan-orphan',
    revision: 12,
    planDocument: {
      id: 'plan-doc-orphan',
      version: 9,
    },
  }
  const payloadNoBinding = buildTaskAcceptancePayload(noBindingTask)
  assert.equal(payloadNoBinding.session_id, 'sess-plan-orphan')
  assert.equal(payloadNoBinding.plan_id, undefined, 'Missing binding must not infer plan_id from planDocument')
  assert.equal(
    payloadNoBinding.definition_revision,
    undefined,
    'Missing binding must remain missing and not infer definition_revision from doc.version or task.revision'
  )

  // Case 4 (Negative test): Binding missing definition revision must NOT fall back to doc.version or task.revision
  const bindingWithoutRevTask = {
    sessionId: 'sess-exec-003',
    revision: 15,
    planBinding: {
      planId: 'plan-auth-v3',
      sessionId: 'sess-plan-003',
    },
    planDocument: {
      id: 'plan-auth-v3',
      version: 5,
    },
  }
  const payloadNoRev = buildTaskAcceptancePayload(bindingWithoutRevTask)
  assert.equal(payloadNoRev.session_id, 'sess-plan-003', 'Must prefer binding.sessionId')
  assert.equal(payloadNoRev.plan_id, 'plan-auth-v3')
  assert.equal(
    payloadNoRev.definition_revision,
    undefined,
    'Binding without definition revision must remain undefined, never falling back to doc.version or task.revision'
  )

  // Case 5: Direct coder task without plan binding
  const coderTask = {
    sessionId: 'sess-coder-002',
    planBinding: undefined,
  }
  const payloadCoder = buildTaskAcceptancePayload(coderTask)
  assert.equal(payloadCoder.session_id, 'sess-coder-002')
  assert.equal(payloadCoder.plan_id, undefined)
  assert.equal(payloadCoder.definition_revision, undefined)

  // Case 6: Empty or null task
  const payloadNull = buildTaskAcceptancePayload(null)
  assert.equal(payloadNull.session_id, undefined)
  assert.equal(payloadNull.plan_id, undefined)
  assert.equal(payloadNull.definition_revision, undefined)
})

test('Desktop target selection omits Auto-detect and preserves explicit project identity', () => {
  // Requirement: project coordination ordering must never become execution authority.
  // Threat: a multi-repository project silently deploys a Coder into its first repository.
  // Boundary: resolveTaskWorkspace/taskWorkspaceSelection used by preview and submit; backend resolves omitted targets.
  // This pure helper layer is the narrowest proof of identical preview/submit selection payloads.
  const project = {
    workspaces: [
      { path: '/coordination', workspace_id: 'ws-coord' },
      { path: '/product', workspace_id: 'ws-product' },
    ],
  }
  assert.equal(resolveTaskWorkspace(''), undefined)
  assert.deepEqual(taskWorkspaceSelection('', project), {}, 'Auto-detect sends no default path or ID')
  assert.deepEqual(taskWorkspaceSelection(' /product ', project), {
    workspace_path: '/product', workspace_id: 'ws-product',
  }, 'explicit selection carries product path and catalog ID')
  // The project catalog may omit generation; when supplied, both request paths
  // must use this same selector so a stale target cannot silently retarget.
  const generationProject = { workspaces: [{ path: '/product', workspace_id: 'ws-product', workspace_generation: 7 }] }
  assert.deepEqual(taskWorkspaceSelection('/product', generationProject), {
    workspace_path: '/product', workspace_id: 'ws-product', workspace_generation: 7,
  }, 'catalog generation guards preview and submit against a stale repository binding')
  assert.deepEqual(taskWorkspaceSelection('', generationProject), {}, 'Auto-detect must not claim catalog identity')
  assert.deepEqual(taskWorkspaceSelection('/product', project), taskWorkspaceSelection(' /product ', project),
    'preview and submit use the same normalized target')
  assert.throws(() => taskWorkspaceSelection('.', project), /dot '\.' is not allowed/)
  assert.throws(() => taskWorkspaceSelection('/stale', project), /not linked/)
  assert.deepEqual(taskWorkspaceSelection('', { workspaces: [{ path: '/only', workspace_id: 'ws-only' }] }), {},
    'even a single repository is resolved by the backend, not selected by UI')
  const mapped = mapBackendTask({ id: 'task', title: 'Change', project_id: 'project',
    workspace_path: '/allocated/worktree', source_workspace: {
      path: '/product', workspace_id: 'ws-product', workspace_generation: 3, provenance: 'explicit',
    },
  })
  assert.equal(mapped.workspaceTarget, '/product', 'source repository label must not show allocated runtime as target')
  assert.equal(mapped.workspacePath, '/allocated/worktree', 'runtime path stays separately available')
  assert.equal(mapped.sourceWorkspaceId, 'ws-product')
  assert.equal(mapped.sourceWorkspaceGeneration, 3)
  const viewSource = fs.readFileSync(path.join(__dirname, 'OrchestrateView.tsx'), 'utf8')
  assert.equal(viewSource.split('taskWorkspaceSelection(newTaskWorkspace, selectedProject)').length - 1, 2,
    'model preview and task submission must share the exact workspace selection helper')
  assert.ok(viewSource.includes('workspaceCatalog: selectedProject?.workspaces'),
    'a changed catalog generation must invalidate the model preview query')
})

test('Task submission retry identity is stable only for an unchanged execution contract', () => {
  // Requirement: transport retries may reuse their ID, but changed target or project must not replay an old reservation.
  // Threat: an ambiguous request or stale target is retried as a different workspace with the same task identity.
  // Boundary: taskDeployRequestIdentity keys the exact payload submitted to project task creation.
  let count = 0
  const nextId = () => `task-${++count}`
  const first = taskDeployRequestIdentity(null, 'project', { prompt: 'change', ...taskWorkspaceSelection('') }, nextId)
  assert.deepEqual(taskDeployRequestIdentity(first, 'project', { prompt: 'change' }, nextId), first)
  const product = taskDeployRequestIdentity(first, 'project', {
    prompt: 'change', ...taskWorkspaceSelection('/product', {
      workspaces: [{ path: '/product', workspace_id: 'ws-product' }],
    }),
  }, nextId)
  assert.notEqual(product.clientTaskId, first.clientTaskId)
  const nextGeneration = taskDeployRequestIdentity(product, 'project', {
    prompt: 'change', ...taskWorkspaceSelection('/product', {
      workspaces: [{ path: '/product', workspace_id: 'ws-product', workspace_generation: 8 }],
    }),
  }, nextId)
  assert.notEqual(nextGeneration.clientTaskId, product.clientTaskId,
    'a changed catalog generation must not replay the old request identity')
  assert.equal(taskDeployRequestIdentity(product, 'project', {
    prompt: 'change', workspace_path: '/product', workspace_id: 'ws-product',
  }, nextId).clientTaskId, product.clientTaskId)
  assert.notEqual(taskDeployRequestIdentity(product, 'another-project', {
    prompt: 'change', workspace_path: '/product', workspace_id: 'ws-product',
  }, nextId).clientTaskId, product.clientTaskId)
})

test('Task Deliverables typing preserves code PR and audit deliverables without media substitution', () => {
  // Requirement: Coding outputs real code PR/commit deliverables, never image/audit substitution;
  // retain media deliverables for image/video/audio and audit reports for finder.

  // Case 1: Code task maps deliverable type to 'code', thumbnail to 'default'
  const codeTaskBackend = {
    id: 't-code-1',
    title: 'Add JWT Middleware',
    agent: 'coder',
    outcome_type: 'code_pr',
    deliverables: [{ id: 'd-1', title: 'Branch PR' }],
    plan_binding: { plan_id: 'p-1', definition_revision: 2, session_id: 's-1' },
    plan_document: { id: 'p-1', title: 'JWT Plan', checkpoints: [] },
  }
  const mappedCode = mapBackendTask(codeTaskBackend)
  assert.equal(mappedCode.deliverables![0].type, 'code', 'Code deliverable must not default to image')
  assert.equal(mappedCode.deliverables![0].thumbnailType, 'default', 'Code deliverable thumbnail must not default to cyber_lattice')
  assert.equal(mappedCode.planBinding?.planId, 'p-1')
  assert.equal(mappedCode.planBinding?.definitionRevision, 2)
  assert.equal(mappedCode.planBinding?.sessionId, 's-1')
  assert.deepEqual(buildTaskAcceptancePayload(mappedCode), {
    session_id: 's-1', plan_id: 'p-1', definition_revision: 2,
  }, 'normalized plan guard must retain authoritative backend revision and session')
  assert.ok(mappedCode.planDocument)

  // Case 2: Finder audit task maps deliverable type to 'report'
  const finderTaskBackend = {
    id: 't-finder-1',
    title: 'Security Audit',
    agent: 'finder',
    outcome_type: 'audit_report',
    deliverables: [{ id: 'd-2', title: 'Audit Report' }],
  }
  const mappedFinder = mapBackendTask(finderTaskBackend)
  assert.equal(mappedFinder.deliverables![0].type, 'report', 'Audit report deliverable must not default to image')

  // Case 3: Image task preserves image deliverable type
  const imageTaskBackend = {
    id: 't-img-1',
    title: 'Logo Design',
    agent: 'image',
    outcome_type: 'media_bundle',
    deliverables: [{ id: 'd-3', title: 'Logo Variant' }],
  }
  const mappedImage = mapBackendTask(imageTaskBackend)
  assert.equal(mappedImage.deliverables![0].type, 'image')
  assert.equal(mappedImage.deliverables![0].thumbnailType, 'cyber_lattice')

  // Case 4: Video task preserves video deliverable type
  const videoTaskBackend = {
    id: 't-vid-1',
    title: 'Intro Video',
    agent: 'video',
    outcome_type: 'video_clip',
    deliverables: [{ id: 'd-4', title: 'Clip 1' }],
  }
  const mappedVideo = mapBackendTask(videoTaskBackend)
  assert.equal(mappedVideo.deliverables![0].type, 'video')
})

test('OrchestrateView source contracts: no premature execution, duplicate-click prevention, authoritative response selection, structured plan rendering, and error recovery', () => {
  // Written test purpose:
  // - Product requirement/invariant: Task acceptance must prevent premature execution, supply exact acceptance
  //   body, prevent duplicate clicks while in-flight, select authoritative returned session ID, render structured
  //   checkpoints/criteria and Task Program stages/jobs, and display visible error/stale recovery.
  // - Regression prevented: Premature status flip before backend confirmation, duplicate approval calls,
  //   selecting stale snapshot session IDs, hiding approval errors in console.warn only, and unreadable plan specs.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // 1. User-visible pending / no premature execution
  assert.ok(
    source.includes('data-testid="task-planning-banner"'),
    'Must render planning state banner when task is in planning status'
  )
  assert.ok(
    source.includes('Plan Mode (Read-Only)'),
    'Planning banner must indicate read-only plan mode'
  )
  assert.ok(
    !source.includes('setOptimisticTasks(selectedProject.id, (prev) => prev.map((t) => t.id === taskId ? { ...t, status: \'in_progress\''),
    'handleApproveTask must NOT prematurely flip task status to in_progress before backend confirmation'
  )

  // 2. Duplicate-click prevention & in-flight guard
  assert.ok(
    source.includes('approvingTaskIds.has(taskId)'),
    'handleApproveTask must guard against concurrent approvals for the same task'
  )
  assert.ok(
    source.includes('data-testid="approve-task-btn"'),
    'Must render approve task button with data-testid="approve-task-btn"'
  )
  assert.ok(
    source.includes('disabled={isApproving}'),
    'Approve button must be disabled when approval is in-flight'
  )
  assert.ok(
    source.includes('Approving & Starting...'),
    'Approve button must show in-flight spinner state'
  )

  // 3. Exact acceptance body with revision guards
  assert.ok(
    source.includes('buildTaskAcceptancePayload(targetTask'),
    'handleApproveTask must use buildTaskAcceptancePayload to construct revision guards'
  )

  // 4. Authoritative backend response link selection
  assert.ok(
    source.includes('const linkedSessionId = res.task.session_id || res.task.sessionId || res.task.plan_binding?.session_id || res.task.planBinding?.session_id') &&
    source.includes('setActiveSessionId(linkedSessionId)') &&
    !source.includes('setActiveSessionId(targetTask.sessionId)'),
    'Must use returned linked session ID from authoritative response, not stale pre-call snapshot'
  )

  // 5. Visible error & stale recovery
  assert.ok(
    source.includes('data-testid="task-error-banner"'),
    'Must render visible task error banner when approval or task actions fail'
  )
  assert.ok(
    source.includes('data-testid="retry-approve-btn"'),
    'Task error banner must provide Retry button for pending approval tasks'
  )
  assert.ok(
    source.includes('data-testid="deploy-modal-error"'),
    'Deploy modal must display visible error banner on missing workspace or deploy failure'
  )

  // 6. Structured plan rendering (checkpoints, tasks, criteria, and Task Program specs)
  assert.ok(
    source.includes('data-testid="task-plan-spec"'),
    'Must render structured plan specification container'
  )
  assert.ok(
    source.includes('data-testid="toggle-plan-spec-btn"'),
    'Must render toggle button for structured plan spec'
  )
  assert.ok(
    source.includes('Review Structured Plan & Acceptance Criteria'),
    'Must provide button label for structured plan review'
  )
  assert.ok(
    source.includes('plan-checkpoint-'),
    'Checkpoints must be rendered as structured list items with checkpoint testids'
  )
  assert.ok(
    source.includes('Acceptance Criteria:'),
    'Structured plan view must render Acceptance Criteria section'
  )
  assert.ok(
    source.includes('data-testid="task-program-spec"'),
    'Must render Task Program specification with stages and jobs'
  )

  // 7. Route & output compatibility
  assert.ok(
    source.includes("deliverableType === 'code' || deliverableType === 'pr'"),
    'Deliverable thumbnail must handle code PR deliverables without image substitution'
  )
  assert.ok(
    source.includes('Executing Coder...'),
    'Code deliverables must show Executing Coder... while generating, not Generating Media...'
  )
  assert.ok(
    source.includes('Code PR • Pending Acceptance'),
    'Pending code deliverables must show Code PR • Pending Acceptance'
  )

  // 8. Idempotent & retry-safe request IDs, pre-flight approval guards, and no premature execution on reopen
  assert.ok(
    source.includes("'X-Request-ID': approveRequestId"),
    'Approve request must include X-Request-ID header'
  )
  assert.ok(
    source.includes("'X-Request-ID': clientTaskId"),
    'Deploy request must include X-Request-ID header'
  )
  assert.ok(
    source.includes('id: clientTaskId'),
    'Deploy request must pass client task ID for idempotent retries'
  )
  assert.ok(
    source.includes('isDeployingTaskRef.current'),
    'Deploy submit must use synchronous ref lock against double-click'
  )
  assert.ok(
    source.includes('approvingTaskIdsRef.current.has(taskId)'),
    'Approve task must use synchronous ref lock against double-click'
  )
  assert.ok(
    source.includes('pendingDeployRequestRef'),
    'Deploy submit must retain stable request identity across retries for unchanged payload'
  )
  assert.ok(
    source.includes('pendingApproveRequestIdsRef'),
    'Approve task must retain request identity across retries'
  )
  assert.ok(
    source.includes('approve:${taskId}:${targetTask.revision || 1}:${acceptanceBody.plan_id}:r${acceptanceBody.definition_revision}'),
    'Approve task must construct exact binding-derived request identity when plan binding is present'
  )
  assert.ok(
    !source.includes('hasReviewRequired || (!isLifecycleActive && sess && (sess.message_count ?? 0) > 1)'),
    'Must not infer task needs_review status from session.message_count'
  )
  assert.ok(
    source.includes("targetTask.status === 'planning'"),
    'handleApproveTask must check planning status before execution'
  )
  assert.ok(
    source.includes("targetTask.status === 'rejected'"),
    'handleApproveTask must check rejected status before execution'
  )
  assert.ok(
    source.includes("planDoc?.status === 'rejected'"),
    'handleApproveTask must check rejected plan document'
  )
  assert.ok(
    source.includes('Plan definition revision guard is missing or stale'),
    'handleApproveTask must enforce plan revision guard'
  )
  // The memo delegates reconciliation to the helper; terminal precedence lives there.
  const helperSource = fs.readFileSync(path.join(__dirname, 'orchestrate-task-helpers.ts'), 'utf8')
  assert.ok(
    source.includes('tasks.map((task) => aggregateTaskLiveState(task, liveTaskSessionsData))'),
    'liveTasks memo must use the canonical session-state aggregator'
  )
  assert.ok(
    helperSource.includes("} else if (task.status === 'rejected') {") &&
      helperSource.includes("status = 'rejected'") &&
      helperSource.includes('if (isAnyFailed) {') &&
      helperSource.includes("status = 'failed'"),
    'aggregateTaskLiveState must retain rejected outcomes and prioritize failure over review'
  )
  assert.ok(
    source.includes('data-testid="task-failed-banner"'),
    'MinimalTaskCard must render failed task banner'
  )
  assert.ok(
    source.includes('data-testid="task-plan-rejected-banner"'),
    'MinimalTaskCard must render plan rejected banner'
  )
  assert.ok(
    source.includes("{ key: 'failed', label: 'Failed', color: 'rose' }"),
    'Kanban must render dedicated Failed column'
  )
  assert.ok(
    source.includes('disabled={isApproving || isPlanRejected || isPlanTaskWithoutStructuredPlan || isPlanBindingMissingRevision}'),
    'Approve button must be disabled for rejected plans or missing structured plans'
  )
})

test('Failed and rejected task retention: terminal outcomes are never overwritten and render visible failure state', () => {
  // Requirement: Failed and rejected tasks must remain visible and retained across all views.
  // Must not be overwritten to needs_review or hidden from Kanban.
  const failedBackend = {
    id: 't-failed-1',
    title: 'Broken Build Task',
    status: 'failed',
    agent: 'coder',
    last_error: 'Compile error: exit status 2',
  }
  const mappedFailed = mapBackendTask(failedBackend)
  assert.equal(mappedFailed.status, 'failed', 'mapBackendTask must preserve failed status')

  const rejectedBackend = {
    id: 't-rejected-1',
    title: 'Disapproved Task',
    status: 'rejected',
    agent: 'coder',
    action_needed: 'Task rejected by user',
  }
  const mappedRejected = mapBackendTask(rejectedBackend)
  assert.equal(mappedRejected.status, 'rejected', 'mapBackendTask must preserve rejected status')
})

