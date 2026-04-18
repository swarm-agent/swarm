import { requestJson } from '../../../../app/api'

interface DeleteSwarmGroupMemberInput {
  groupID: string
  swarmID: string
}

export async function deleteSwarmGroupMember(input: DeleteSwarmGroupMemberInput): Promise<void> {
  await requestJson('/v1/swarm/groups/members/delete', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      group_id: input.groupID,
      swarm_id: input.swarmID,
    }),
  })
}
