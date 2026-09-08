// Match PendingVideoEditProposalForRevision: only the exact pending working cut
// is blocked. Old pending records remain history, never implicit acceptance.
export type ProposalIdentity = { id: string; status: string; working_revision_id?: string; project_id: string; session_id?: string }
export type RevisionIdentity = { id: string; project_id: string; session_id?: string }

export function pendingProposalForRevision<T extends ProposalIdentity>(proposals: T[], revision: RevisionIdentity | null | undefined): T | null {
  if (!revision?.id) return null
  return proposals.find((proposal) => proposal.status === 'pending' && proposal.working_revision_id === revision.id
    && proposal.project_id === revision.project_id && (!proposal.session_id || proposal.session_id === revision.session_id)) ?? null
}

export function classifyVideoProposals<T extends ProposalIdentity>(proposals: T[], revision: RevisionIdentity | null | undefined) {
  const current = pendingProposalForRevision(proposals, revision)
  const scoped = proposals.filter((proposal) => revision && proposal.project_id === revision.project_id && (!proposal.session_id || proposal.session_id === revision.session_id))
  return { current, stale: scoped.filter((proposal) => proposal.status === 'pending' && proposal !== current), historical: scoped.filter((proposal) => proposal.status !== 'pending') }
}
