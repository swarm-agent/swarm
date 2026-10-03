type ProjectIdentity = { id: string; name: string }

/** Browser labels only. API requests, ownership and stored selection always use id. */
export function projectNameSlug(name: string): string {
  return name.normalize('NFKD').replace(/\p{M}/gu, '').toLowerCase()
    .replace(/[^\p{L}\p{N}]+/gu, '-').replace(/^-+|-+$/g, '') || 'project'
}

export function projectRouteSegment(project: ProjectIdentity, projects: readonly ProjectIdentity[]): string {
  const slug = projectNameSlug(project.name)
  const conflicts = projects.some(other => other.id !== project.id && (projectNameSlug(other.name) === slug || other.id === slug))
  const candidate = conflicts ? `${slug}--${project.id}` : slug
  // Never shadow an existing opaque ID or another project's literal name.
  return projects.some(other => other.id !== project.id && (other.id === candidate || projectNameSlug(other.name) === candidate))
    ? project.id : candidate
}

export function resolveProjectRoute<T extends ProjectIdentity>(segment: string, projects: readonly T[]): T | undefined {
  // Old bookmarks retain priority over human-readable aliases.
  const byId = projects.find(project => project.id === segment)
  if (byId) return byId
  const matches = projects.filter(project => projectRouteSegment(project, projects) === segment)
  return matches.length === 1 ? matches[0] : undefined
}
