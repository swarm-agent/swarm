import { requestJson } from '../../../../app/api'

interface UpsertSwarmGroupInput {
  groupID?: string
  name: string
  networkName?: string
  setCurrent?: boolean
}

export async function upsertSwarmGroup(input: UpsertSwarmGroupInput): Promise<void> {
  await requestJson('/v1/swarm/groups/upsert', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      group_id: input.groupID,
      name: input.name,
      network_name: input.networkName,
      set_current: Boolean(input.setCurrent),
    }),
  })
}
