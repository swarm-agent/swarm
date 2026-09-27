import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  getPrimarySystemAgentName,
  isMediaTaskType,
  resolveDeployImpendingConfig,
  resolveOptimisticApprovedDeliverables,
  resolveTaskImpendingAgents,
} from './orchestrate-task-helpers'
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
