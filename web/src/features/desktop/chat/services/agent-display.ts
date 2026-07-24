const COMPILED_SYSTEM_AGENT_LABELS: Readonly<Record<string, string>> = {
  'system-explorer': 'Explorer',
  'system-coder': 'Coder',
  'system-designer': 'Designer',
  'system-clone': 'Coder', // Historical durable sessions only; new launches use system-coder.
  coder: 'Coder',
  swarm: 'Swarm',
}

export function displayAgentName(name: string): string {
  const trimmed = name.trim()
  return COMPILED_SYSTEM_AGENT_LABELS[trimmed] ?? trimmed
}
