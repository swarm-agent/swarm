import z from "zod"
import { Instance } from "../project/instance"
import { Log } from "../util/log"
import { NamedError } from "../util/error"
import { Bus } from "../bus"

export namespace SessionLock {
  const log = Log.create({ service: "session.lock" })

  export const LockedError = NamedError.create(
    "SessionLockedError",
    z.object({
      sessionID: z.string(),
      message: z.string(),
    }),
  )

  export const Event = {
    Completed: Bus.event(
      "session.completed",
      z.object({
        sessionID: z.string(),
        timestamp: z.number(),
      }),
    ),
    Aborted: Bus.event(
      "session.aborted",
      z.object({
        sessionID: z.string(),
        timestamp: z.number(),
      }),
    ),
    AgentSwitch: Bus.event(
      "session.agent_switch",
      z.object({
        sessionID: z.string(),
        agent: z.string(),
        timestamp: z.number(),
      }),
    ),
  }

  type LockState = {
    controller: AbortController
    created: number
    pendingAgentSwitch?: string
    pendingGracefulSwitch?: string
  }

  const state = Instance.state(
    () => {
      const locks = new Map<string, LockState>()
      return {
        locks,
      }
    },
    async (current) => {
      for (const [sessionID, lock] of current.locks) {
        log.info("force abort", { sessionID })
        lock.controller.abort()
      }
      current.locks.clear()
    },
  )

  function get(sessionID: string) {
    return state().locks.get(sessionID)
  }

  function unset(input: { sessionID: string; controller: AbortController }) {
    const lock = get(input.sessionID)
    if (!lock) return false
    if (lock.controller !== input.controller) return false
    state().locks.delete(input.sessionID)
    return true
  }

  export function acquire(input: { sessionID: string }) {
    const lock = get(input.sessionID)
    if (lock) {
      throw new LockedError({
        sessionID: input.sessionID,
        message: `Session ${input.sessionID} is locked`,
      })
    }
    const controller = new AbortController()
    state().locks.set(input.sessionID, {
      controller,
      created: Date.now(),
    })
    log.info("locked", { sessionID: input.sessionID })
    return {
      signal: controller.signal,
      abort() {
        controller.abort()
        unset({ sessionID: input.sessionID, controller })
        Bus.publish(Event.Aborted, {
          sessionID: input.sessionID,
          timestamp: Date.now(),
        })
      },
      async [Symbol.dispose]() {
        const removed = unset({ sessionID: input.sessionID, controller })
        if (removed) {
          log.info("unlocked", { sessionID: input.sessionID })
          Bus.publish(Event.Completed, {
            sessionID: input.sessionID,
            timestamp: Date.now(),
          })
        }
      },
    }
  }

  export function abort(sessionID: string) {
    const lock = get(sessionID)
    if (!lock) return false
    log.info("abort", { sessionID })
    lock.controller.abort()
    state().locks.delete(sessionID)
    Bus.publish(Event.Aborted, {
      sessionID,
      timestamp: Date.now(),
    })
    return true
  }

  export function isLocked(sessionID: string) {
    return get(sessionID) !== undefined
  }

  export function assertUnlocked(sessionID: string) {
    const lock = get(sessionID)
    if (!lock) return
    throw new LockedError({ sessionID, message: `Session ${sessionID} is locked` })
  }

  export function switchAgent(sessionID: string, agent: string) {
    const lock = get(sessionID)
    if (!lock) return false
    log.info("switch agent", { sessionID, agent })
    // Store pending switch before aborting (abort deletes the lock)
    setPendingSwitch(sessionID, agent)
    lock.controller.abort()
    state().locks.delete(sessionID)
    Bus.publish(Event.AgentSwitch, {
      sessionID,
      agent,
      timestamp: Date.now(),
    })
    return true
  }

  export function consumeAgentSwitch(sessionID: string): string | undefined {
    const pending = state().locks.get(sessionID + ":pending_switch")
    if (pending) {
      state().locks.delete(sessionID + ":pending_switch")
      return pending.pendingAgentSwitch
    }
    return undefined
  }

  export function setPendingSwitch(sessionID: string, agent: string) {
    state().locks.set(sessionID + ":pending_switch", {
      controller: new AbortController(),
      created: Date.now(),
      pendingAgentSwitch: agent,
    })
  }

  export function requestGracefulSwitch(sessionID: string, agent: string): boolean {
    const lock = get(sessionID)
    if (!lock) return false
    log.info("request graceful agent switch", { sessionID, agent })
    lock.pendingGracefulSwitch = agent
    Bus.publish(Event.AgentSwitch, {
      sessionID,
      agent,
      timestamp: Date.now(),
    })
    return true
  }

  export function consumeGracefulSwitch(sessionID: string): string | undefined {
    const lock = get(sessionID)
    if (!lock?.pendingGracefulSwitch) return undefined
    const agent = lock.pendingGracefulSwitch
    lock.pendingGracefulSwitch = undefined
    return agent
  }
}
