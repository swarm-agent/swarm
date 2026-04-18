import type { ComponentType } from 'react'
import { WorkspaceLauncherIteration1 } from './iterations/workspace-launcher-iteration-1'
import type { WorkspaceLauncherIterationDefinition, WorkspaceLauncherIterationId, WorkspaceLauncherIterationProps } from './iterations/types'

export type { WorkspaceLauncherIterationDefinition, WorkspaceLauncherIterationId, WorkspaceLauncherIterationProps } from './iterations/types'

export const WORKSPACE_LAUNCHER_ITERATIONS: WorkspaceLauncherIterationDefinition[] = [
  { id: '1', label: '1', tagline: 'Base workspace launcher.' },
]

export const WORKSPACE_LAUNCHER_ITERATION_COMPONENTS: Record<WorkspaceLauncherIterationId, ComponentType<WorkspaceLauncherIterationProps>> = {
  '1': WorkspaceLauncherIteration1,
}
