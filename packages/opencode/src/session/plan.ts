import z from "zod"
import { Bus } from "../bus"
import { Identifier } from "../id/id"
import { Storage } from "../storage/storage"
import { Log } from "../util/log"

export namespace SessionPlan {
  const log = Log.create({ service: "session-plan" })

  export const Status = z.enum(["pending", "approved", "rejected", "editing"])
  export type Status = z.infer<typeof Status>

  export const Info = z
    .object({
      id: Identifier.schema("plan"),
      sessionID: z.string(),
      content: z.string(),
      summary: z.string().optional(),
      status: Status,
      time: z.object({
        created: z.number(),
        updated: z.number(),
      }),
      rejectionMessage: z.string().optional(),
    })
    .meta({
      ref: "SessionPlan",
    })
  export type Info = z.output<typeof Info>

  export const Event = {
    Updated: Bus.event(
      "session-plan.updated",
      z.object({
        info: Info,
      }),
    ),
  }

  export async function save(sessionID: string, content: string): Promise<Info> {
    const now = Date.now()
    const info: Info = {
      id: Identifier.ascending("plan"),
      sessionID,
      content,
      status: "pending",
      time: {
        created: now,
        updated: now,
      },
    }

    log.info("saving plan", { sessionID, planID: info.id })
    await Storage.write(["plan", sessionID], info)
    Bus.publish(Event.Updated, { info })
    return info
  }

  export async function get(sessionID: string): Promise<Info | null> {
    const info = await Storage.read<Info>(["plan", sessionID]).catch(() => null)
    return info
  }

  export async function update(
    sessionID: string,
    updates: Partial<Pick<Info, "content" | "status" | "rejectionMessage" | "summary">>,
  ): Promise<Info> {
    const existing = await get(sessionID)
    if (!existing) {
      throw new Error(`No plan found for session ${sessionID}`)
    }

    const updated: Info = {
      ...existing,
      ...updates,
      time: {
        ...existing.time,
        updated: Date.now(),
      },
    }

    log.info("updating plan", { sessionID, planID: updated.id, status: updated.status })
    await Storage.write(["plan", sessionID], updated)
    Bus.publish(Event.Updated, { info: updated })
    return updated
  }

  export async function remove(sessionID: string): Promise<void> {
    log.info("removing plan", { sessionID })
    await Storage.remove(["plan", sessionID])
  }
}
