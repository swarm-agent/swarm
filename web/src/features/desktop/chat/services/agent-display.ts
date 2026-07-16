const COMPILED_SYSTEM_AGENT_LABELS: Readonly<Record<string, string>> = {
  'system-explorer': 'Explorer',
  'system-clone': 'Clone',
  swarm: 'Swarm',
}

export function displayAgentName(name: string): string {
  const trimmed = name.trim()
  return COMPILED_SYSTEM_AGENT_LABELS[trimmed] ?? trimmed
}
