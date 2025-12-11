import z from "zod"
import { Bus } from "../bus"
import { Log } from "../util/log"
import { Identifier } from "../id/id"
import { Plugin } from "../plugin"
import { Instance } from "../project/instance"
import { Wildcard } from "../util/wildcard"
import { Session } from "../session"
import * as fs from "fs/promises"
import * as path from "path"
import { parse as parseJsonc } from "jsonc-parser"
import { Pin } from "../auth/pin"
import { Hyprland } from "../hyprland"
import { Notification } from "../notification"

export namespace Permission {
  const log = Log.create({ service: "permission" })

  function toKeys(pattern: Info["pattern"], type: string): string[] {
    return pattern === undefined ? [type] : Array.isArray(pattern) ? pattern : [pattern]
  }

  function covered(keys: string[], approved: Record<string, boolean>): boolean {
    const pats = Object.keys(approved)
    return keys.every((k) => pats.some((p) => Wildcard.match(k, p)))
  }

  async function findConfigPath(): Promise<string> {
    const configFiles = ["opencode.json", "opencode.jsonc"]
    for (const file of configFiles) {
      const testPath = path.join(Instance.directory, file)
      await fs.access(testPath).catch(() => {})
      const exists = await fs
        .access(testPath)
        .then(() => true)
        .catch(() => false)
      if (exists) return testPath
    }
    return path.join(Instance.directory, "opencode.json")
  }

  async function readConfig(configPath: string): Promise<Record<string, unknown>> {
    return fs
      .readFile(configPath, "utf-8")
      .then((text) => parseJsonc(text) as Record<string, unknown>)
      .catch((err: NodeJS.ErrnoException) => {
        if (err.code === "ENOENT") return {}
        log.error("failed to read config", { path: configPath, error: err })
        throw err
      })
  }

  function updateConfigWithPermission(
    config: Record<string, unknown>,
    type: string,
    pattern: string | string[] | undefined,
  ): Record<string, unknown> {
    const updated = { ...config }
    updated.permission = updated.permission || {}
    const permissions = updated.permission as Record<string, unknown>

    if (type === "bash" && pattern !== undefined) {
      const patterns = Array.isArray(pattern) ? pattern : [pattern]
      const bashPerms = typeof permissions.bash === "string" ? {} : (permissions.bash as Record<string, string>) || {}
      permissions.bash = { ...bashPerms }
      for (const p of patterns) {
        ;(permissions.bash as Record<string, string>)[p] = "allow"
      }
    } else if (type === "network" && pattern !== undefined) {
      // Network permissions for sandbox - store in sandbox.network.allowedDomains
      const patterns = Array.isArray(pattern) ? pattern : [pattern]
      updated.sandbox = updated.sandbox || {}
      const sandbox = updated.sandbox as Record<string, unknown>
      sandbox.network = sandbox.network || {}
      const network = sandbox.network as Record<string, unknown>
      const existing = (network.allowedDomains as string[]) || []
      network.allowedDomains = [...new Set([...existing, ...patterns])]
    } else if (type === "edit" || type === "webfetch") {
      permissions[type] = "allow"
    } else {
      log.warn("unknown permission type", { type, pattern })
    }

    return updated
  }

  async function persistPermission(type: string, pattern: string | string[] | undefined) {
    const configPath = await findConfigPath()
    const config = await readConfig(configPath).catch(() => ({}))
    const updated = updateConfigWithPermission(config, type, pattern)

    await fs
      .writeFile(configPath, JSON.stringify(updated, null, 2), "utf-8")
      .then(() => log.info("persisted permission to config", { path: configPath, type, pattern }))
      .catch((err) => log.error("failed to persist permission", { type, pattern, error: err }))
  }

  export const Info = z
    .object({
      id: z.string(),
      type: z.string(),
      pattern: z.union([z.string(), z.array(z.string())]).optional(),
      sessionID: z.string(),
      messageID: z.string(),
      callID: z.string().optional(),
      title: z.string(),
      metadata: z.record(z.string(), z.any()),
      time: z.object({
        created: z.number(),
      }),
    })
    .meta({
      ref: "Permission",
    })
  export type Info = z.infer<typeof Info>

  export const Response = z.union([
    z.literal("once"),
    z.literal("always"),
    z.literal("reject"),
    z.object({ type: z.literal("reject"), message: z.string() }),
    // once with message - for approve with comment (Shift+Enter in plan mode)
    z.object({ type: z.literal("once"), message: z.string() }),
    // answers variant MUST come before agent variant since agent is optional
    // and zod union matches first valid schema
    z.object({
      type: z.literal("once"),
      answers: z.record(z.string(), z.union([z.string(), z.array(z.string())])),
    }),
    z.object({ type: z.enum(["once", "always"]), agent: z.string().optional() }),
    // PIN verification response
    z.object({ type: z.literal("pin"), pin: z.string() }),
  ])
  export type Response = z.infer<typeof Response>

  export const Event = {
    Updated: Bus.event("permission.updated", Info),
    Replied: Bus.event(
      "permission.replied",
      z.object({
        sessionID: z.string(),
        permissionID: z.string(),
        response: Response,
      }),
    ),
  }

  const state = Instance.state(
    () => {
      const pending: {
        [sessionID: string]: {
          [permissionID: string]: {
            info: Info
            resolve: () => void
            reject: (e: any) => void
          }
        }
      } = {}

      const approved: {
        [sessionID: string]: {
          [permissionID: string]: boolean
        }
      } = {}

      return {
        pending,
        approved,
      }
    },
    async (state) => {
      for (const pending of Object.values(state.pending)) {
        for (const item of Object.values(pending)) {
          item.reject(new RejectedError(item.info.sessionID, item.info.id, item.info.callID, item.info.metadata))
        }
      }
    },
  )

  export function pending() {
    return state().pending
  }

  export async function ask(input: {
    type: Info["type"]
    title: Info["title"]
    pattern?: Info["pattern"]
    callID?: Info["callID"]
    sessionID: Info["sessionID"]
    messageID: Info["messageID"]
    metadata: Info["metadata"]
  }): Promise<Info | void> {
    const { pending, approved } = state()
    log.info("asking", {
      sessionID: input.sessionID,
      messageID: input.messageID,
      toolCallID: input.callID,
      pattern: input.pattern,
    })
    const approvedForSession = approved[input.sessionID] || {}
    const keys = toKeys(input.pattern, input.type)
    if (covered(keys, approvedForSession)) return

    // Check parent session's approvals for inheritance (background agents inherit from parent)
    const session = await Session.get(input.sessionID).catch(() => null)
    if (session?.parentID) {
      const approvedForParent = approved[session.parentID] || {}
      if (covered(keys, approvedForParent)) {
        // Auto-approve and cache in child session for future checks
        approved[input.sessionID] = approved[input.sessionID] || {}
        for (const k of keys) {
          approved[input.sessionID][k] = true
        }
        log.info("inherited approval from parent", {
          sessionID: input.sessionID,
          parentID: session.parentID,
          keys,
        })
        return
      }
    }

    const info: Info = {
      id: Identifier.ascending("permission"),
      type: input.type,
      pattern: input.pattern,
      sessionID: input.sessionID,
      messageID: input.messageID,
      callID: input.callID,
      title: input.title,
      metadata: input.metadata,
      time: {
        created: Date.now(),
      },
    }

    switch (
      await Plugin.trigger("permission.ask", info, {
        status: "ask",
      }).then((x) => x.status)
    ) {
      case "deny":
        throw new RejectedError(info.sessionID, info.id, info.callID, info.metadata)
      case "allow":
        return info
    }

    pending[input.sessionID] = pending[input.sessionID] || {}
    log.info("ask: storing in pending", {
      sessionID: input.sessionID,
      permissionID: info.id,
      metadataRef: Object.keys(info.metadata).join(","),
    })

    // Set status to blocked while waiting for user input
    await Hyprland.setStatus("blocked")

    // Send notification after delay if still blocked
    const reason = info.title || `${info.type} permission`
    await Notification.blocked(reason)

    try {
      await new Promise<void>((resolve, reject) => {
        pending[input.sessionID][info.id] = {
          info,
          resolve,
          reject,
        }
        Bus.publish(Event.Updated, info)

        // Forward permission to parent session if this is a child session
        Session.get(input.sessionID)
          .then((session) => {
            if (session.parentID) {
              const forwardedInfo: Info = {
                ...info,
                sessionID: session.parentID,
                metadata: {
                  ...info.metadata,
                  originSessionID: input.sessionID,
                  originSessionTitle: session.title,
                },
              }
              // Add to parent's pending list as well so responses work
              pending[session.parentID] = pending[session.parentID] || {}
              pending[session.parentID][info.id] = {
                info: forwardedInfo,
                resolve, // Same resolve/reject as child - they should be linked
                reject,
              }
              Bus.publish(Event.Updated, forwardedInfo)
            }
          })
          .catch(() => {
            // Session not found, ignore
          })
      })
    } finally {
      // Set status back to working after user responds
      await Hyprland.setStatus("working")
      await Notification.unblocked()
    }

    log.info("permission resolved", {
      permissionID: info.id,
      metadataKeys: Object.keys(info.metadata),
      answers: info.metadata?.answers,
      metadataSnapshot: JSON.stringify(info.metadata),
    })
    // Return the permission info with updated metadata after it resolves
    return info
  }

  export async function respond(input: { sessionID: Info["sessionID"]; permissionID: Info["id"]; response: Response }) {
    log.info("respond called", {
      sessionID: input.sessionID,
      permissionID: input.permissionID,
      response: input.response,
    })
    const { pending, approved } = state()
    log.info("respond pending state", {
      pendingSessions: Object.keys(pending),
      pendingForSession: pending[input.sessionID] ? Object.keys(pending[input.sessionID]) : [],
    })
    const match = pending[input.sessionID]?.[input.permissionID]
    if (!match) {
      log.warn("respond: no match found", { sessionID: input.sessionID, permissionID: input.permissionID })
      return
    }
    log.info("respond: match found", { infoId: match.info.id })

    // Handle PIN verification response
    if (typeof input.response === "object" && "pin" in input.response && input.response.type === "pin") {
      const pinValid = await Pin.verify(input.response.pin)
      log.info("respond: PIN verification", { valid: pinValid })

      // Remove from pending
      delete pending[input.sessionID][input.permissionID]
      Bus.publish(Event.Replied, {
        sessionID: input.sessionID,
        permissionID: input.permissionID,
        response: input.response,
      })

      if (pinValid) {
        match.resolve()
      } else {
        match.reject(
          new RejectedError(input.sessionID, input.permissionID, match.info.callID, match.info.metadata, "Invalid PIN"),
        )
      }
      return
    }

    // Check if this is a forwarded permission from a child session
    const originSessionID = match.info.metadata?.originSessionID as string | undefined

    // If agent is provided in response, update permission metadata
    if (typeof input.response === "object" && "agent" in input.response && input.response.agent) {
      match.info.metadata.selectedAgent = input.response.agent
    }

    // If message is provided in response (for approve-with-comment), update permission metadata
    if (typeof input.response === "object" && "message" in input.response && input.response.message) {
      match.info.metadata.userMessage = input.response.message
    }

    // If answers are provided in response (for ask-user tool), update permission metadata
    if (typeof input.response === "object" && "answers" in input.response && input.response.answers) {
      log.info("respond: setting answers", { answers: input.response.answers })
      match.info.metadata.answers = input.response.answers
      log.info("respond: metadata after setting answers", { metadata: match.info.metadata })
    } else {
      log.info("respond: no answers in response", {
        responseType: typeof input.response,
        hasAnswers: typeof input.response === "object" && "answers" in input.response,
      })
    }

    // Remove from current session's pending list
    delete pending[input.sessionID][input.permissionID]
    Bus.publish(Event.Replied, {
      sessionID: input.sessionID,
      permissionID: input.permissionID,
      response: input.response,
    })

    // If this was forwarded from a child, also remove from child's pending list AND sync metadata
    if (originSessionID && pending[originSessionID]?.[input.permissionID]) {
      const originMatch = pending[originSessionID][input.permissionID]
      // Sync metadata updates (like answers, selectedAgent, userMessage) to the original info object
      if (typeof input.response === "object" && "answers" in input.response && input.response.answers) {
        originMatch.info.metadata.answers = input.response.answers
        log.info("respond: synced answers to origin session", { originSessionID, answers: input.response.answers })
      }
      if (typeof input.response === "object" && "agent" in input.response && input.response.agent) {
        originMatch.info.metadata.selectedAgent = input.response.agent
      }
      if (typeof input.response === "object" && "message" in input.response && input.response.message) {
        originMatch.info.metadata.userMessage = input.response.message
      }
      delete pending[originSessionID][input.permissionID]
      Bus.publish(Event.Replied, {
        sessionID: originSessionID,
        permissionID: input.permissionID,
        response: input.response,
      })
    }

    const isReject =
      input.response === "reject" || (typeof input.response === "object" && input.response.type === "reject")
    const rejectionMessage =
      typeof input.response === "object" && input.response.type === "reject" ? input.response.message : undefined

    if (isReject) {
      match.reject(
        new RejectedError(
          input.sessionID,
          input.permissionID,
          match.info.callID,
          match.info.metadata,
          rejectionMessage,
        ),
      )
      return
    }
    match.resolve()
    if (input.response === "always") {
      approved[input.sessionID] = approved[input.sessionID] || {}
      const approveKeys = toKeys(match.info.pattern, match.info.type)
      for (const k of approveKeys) {
        approved[input.sessionID][k] = true
      }

      // Also approve for the origin session if this was forwarded
      if (originSessionID) {
        approved[originSessionID] = approved[originSessionID] || {}
        for (const k of approveKeys) {
          approved[originSessionID][k] = true
        }
      }

      // Persist the permission to config file
      persistPermission(match.info.type, match.info.pattern).catch((err) => {
        log.error("failed to persist permission", { error: err })
      })

      const items = pending[input.sessionID]
      if (!items) return
      for (const item of Object.values(items)) {
        const itemKeys = toKeys(item.info.pattern, item.info.type)
        if (covered(itemKeys, approved[input.sessionID])) {
          respond({
            sessionID: item.info.sessionID,
            permissionID: item.info.id,
            response: input.response,
          })
        }
      }
    }
  }

  export class RejectedError extends Error {
    constructor(
      public readonly sessionID: string,
      public readonly permissionID: string,
      public readonly toolCallID?: string,
      public readonly metadata?: Record<string, any>,
      public readonly customMessage?: string,
    ) {
      // Generate appropriate message based on context
      const planID = metadata?.planID as string | undefined
      let message: string

      if (planID) {
        // Plan rejection - include planID for re-submission
        if (customMessage) {
          message = `Plan rejected. User feedback: ${customMessage}\n\nUpdate the plan based on feedback and re-submit using planID: ${planID}`
        } else {
          message = `Plan rejected by user. Modify the plan based on context and re-submit using planID: ${planID}`
        }
      } else {
        // Regular tool rejection
        message =
          customMessage || `The user rejected this tool call. DO NOT CALL MORE TOOLS! Wait for further instructions.`
      }

      super(message)
    }
  }
}
