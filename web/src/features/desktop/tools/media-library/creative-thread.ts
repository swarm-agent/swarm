/** Presentation-only lineage. Request IDs own turns; exact output parents own edges.
 * Missing or ambiguous parents never join otherwise independent creations.
 */
export interface CreativeLineageNode<T> {
  id: string
  value: T
  outputs: readonly string[]
  parents: readonly string[]
}
export interface CreativeThread<T> { id: string; turns: T[] }
export function orderCreativeTurns<T>(nodes: readonly CreativeLineageNode<T>[]): T[] {
  const remaining = new Map(nodes.map(node => [node.id, node]))
  const ordered: T[] = []
  while (remaining.size) {
    const outputs = new Map<string, string>()
    for (const node of remaining.values()) for (const output of node.outputs) outputs.set(output, node.id)
    const next = [...remaining.values()].find(node => node.parents.every(parent => !outputs.has(parent) || outputs.get(parent) === node.id)) ?? remaining.values().next().value!
    ordered.push(next.value)
    remaining.delete(next.id)
  }
  return ordered
}
export function creativeThreads<T>(input: readonly CreativeLineageNode<T>[]): CreativeThread<T>[] {
  const nodes = new Map(input.map(node => [node.id, node]))
  const owners = new Map<string, Set<string>>()
  for (const node of nodes.values()) for (const output of node.outputs) {
    if (!output) continue
    const ids = owners.get(output) ?? new Set<string>()
    ids.add(node.id)
    owners.set(output, ids)
  }
  const roots = new Map<string, string>()
  const cyclic = new Set<string>()
  function root(id: string, visiting = new Set<string>()): string {
    if (roots.has(id)) return roots.get(id)!
    if (visiting.has(id)) {
      for (const member of visiting) cyclic.add(member)
      return id
    }
    visiting.add(id)
    const parents = new Set<string>()
    let ambiguous = false
    for (const parent of nodes.get(id)!.parents) {
      const candidates = [...(owners.get(parent) ?? [])].filter(owner => owner !== id)
      if (candidates.length === 1) parents.add(root(candidates[0], new Set(visiting)))
      else if (candidates.length > 1) ambiguous = true
    }
    // Multi-source requests must not silently fuse separate threads.
    const result = !ambiguous && !cyclic.has(id) && parents.size === 1 ? [...parents][0] : id
    roots.set(id, result)
    return result
  }
  const groups = new Map<string, CreativeLineageNode<T>[]>()
  for (const node of nodes.values()) {
    const key = root(node.id)
    groups.set(key, [...(groups.get(key) ?? []), node])
  }
  return [...groups].map(([id, members]) => ({ id, turns: orderCreativeTurns(members) }))
}
