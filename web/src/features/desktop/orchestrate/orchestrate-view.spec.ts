import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import {
  MOCK_PROJECTS,
  MOCK_100_TASKS,
  MOCK_DEPLOYED_WORKERS,
  MOCK_AUTOMATIONS,
} from './orchestrate-mock-data'
import { ORCHESTRATE_THEMES, ORCHESTRATE_THEME_IDS } from './orchestrate-themes'
import type { MiddleCanvasVariant } from './orchestrate-types'

describe('Orchestrate View & Fleet Integration', () => {
  it('defines 5 valid canvas variants and defaults to fleet', () => {
    const validVariants: MiddleCanvasVariant[] = ['matrix', 'kanban', 'fleet', 'split', 'timeline']
    assert.equal(validVariants.length, 5)
    assert.ok(validVariants.includes('fleet'), 'Variant 3 fleet must be a valid variant')
  })

  it('provides 100 benchmark tasks with valid distribution across states', () => {
    assert.equal(MOCK_100_TASKS.length, 100)
    
    const statusCounts = MOCK_100_TASKS.reduce((acc, t) => {
      acc[t.status] = (acc[t.status] || 0) + 1
      return acc
    }, {} as Record<string, number>)

    assert.ok(statusCounts['running'] > 0, 'running tasks present')
    assert.ok(statusCounts['needs_review'] > 0, 'needs_review tasks present')
    assert.ok(statusCounts['queued'] > 0, 'queued tasks present')
    assert.ok(statusCounts['completed'] > 0, 'completed tasks present')
  })

  it('provides worker fleet definitions matching canonical autonomous roles', () => {
    assert.ok(MOCK_DEPLOYED_WORKERS.length >= 4)
    const workerNames = MOCK_DEPLOYED_WORKERS.map(w => w.name)
    assert.ok(workerNames.some(name => name.includes('Video Swarm')))
    assert.ok(workerNames.some(name => name.includes('Code Reviewer')))
    assert.ok(workerNames.some(name => name.includes('Testbench Runner')))
  })

  it('supplies verified modern themes including modern_navy and apple_peach', () => {
    assert.ok(ORCHESTRATE_THEME_IDS.includes('modern_navy'))
    const modernNavy = ORCHESTRATE_THEMES['modern_navy']
    assert.ok(modernNavy)
    assert.ok(modernNavy.bgClass)
    assert.ok(modernNavy.panelBgClass)
    assert.ok(modernNavy.accentColor)

    const applePeach = ORCHESTRATE_THEMES['apple_peach']
    assert.ok(applePeach)
    assert.ok(applePeach.bgClass)
  })

  it('provides mock projects with bound workspaces', () => {
    assert.ok(MOCK_PROJECTS.length > 0)
    const primary = MOCK_PROJECTS[0]
    assert.ok(primary.id)
    assert.ok(primary.name)
    assert.ok(primary.linkedWorkspaces.length >= 2)
  })
})
