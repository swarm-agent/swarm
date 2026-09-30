import type { SwarmPage } from './swarm-navigation'

// Orchestrate owns this catalog. No chat aliases or executable actions belong here.
export const ORCHESTRATE_COMMANDS = [
  { name: 'help', page: 'help', description: 'Learn the Orchestrate workflow' },
  { name: 'projects', page: 'projects', description: 'Select or create a project' },
  { name: 'tasks', page: 'home', description: 'Review the current project’s task cards' },
  { name: 'workers', page: 'workers', description: 'Review durable workers and their jobs' },
  { name: 'deliverables', page: 'deliverables', description: 'Review project outputs' },
  { name: 'agents', page: 'agents', description: 'Configure account-wide system role models' },
  { name: 'settings', page: 'settings', description: 'Account providers, safety and preferences' },
] as const satisfies readonly { name: string; page: SwarmPage; description: string }[]
export type OrchestrateCommand = typeof ORCHESTRATE_COMMANDS[number]

// A leading /word followed by whitespace or end is command-shaped. Paths (/src/a),
// URLs, code, and embedded slash prose are messages. Arguments/multiword commands
// are never dispatched or forwarded to the AI. Case and outer whitespace normalize.
export function parseOrchestrateCommand(text: string):
  | { kind: 'message' }
  | { kind: 'unsupported'; token: string }
  | { kind: 'command'; command: OrchestrateCommand } {
  const normalized = text.trim()
  const match = /^\/([a-z][a-z0-9-]*)(?=\s|$)/i.exec(normalized)
  if (!match) return { kind: 'message' }
  const command = ORCHESTRATE_COMMANDS.find((entry) => entry.name === match[1].toLowerCase())
  if (!command || normalized.slice(match[0].length).trim()) return { kind: 'unsupported', token: match[0] }
  return { kind: 'command', command }
}

export function filterOrchestrateCommands(query: string) {
  const term = query.trim().replace(/^\//, '').toLowerCase()
  if (['actions', 'artifact', 'commit', 'integrate', 'plan', 'task'].includes(term)) return []
  return ORCHESTRATE_COMMANDS.filter((command) => command.name.includes(term) || command.description.toLowerCase().includes(term))
}

export const ORCHESTRATE_TIPS = [
  'Select a project first. Tasks and deliverables belong to the selected project.',
  'Review task cards before approving work. Select a task to discuss it with the Orchestrator.',
  'Workers are durable jobs; Agents are compiled system roles with account-wide model assignments.',
  'Use Agents to configure Swarm, Coder and Orchestrator. Orchestrator shares its assignment with Plan across the account.',
  'Settings contains account-wide providers, permissions, vault and preferences. Project Charter remains project-scoped.',
  'Commands only navigate. They never send a message, start a run or execute an action. Your draft and attachments stay in the conversation.',
] as const
