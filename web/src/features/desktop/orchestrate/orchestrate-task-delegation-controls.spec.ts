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
import type { RunningTask } from './orchestrate-types'
import type { AgentModelSettings } from '../settings/swarm/types/agent-model-settings'

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
})
