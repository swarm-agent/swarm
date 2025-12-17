import z from "zod"
import { spawn } from "child_process"
import { Tool } from "./tool"
import DESCRIPTION from "./bash.txt"
import { Log } from "../util/log"
import { Instance } from "../project/instance"
import { lazy } from "@/util/lazy"
import { Language } from "web-tree-sitter"
import { Agent } from "@/agent/agent"
import { $ } from "bun"
import { Filesystem } from "@/util/filesystem"
import { Wildcard } from "@/util/wildcard"
import { Permission } from "@/permission"
import { Sandbox } from "@/sandbox"
import { Config } from "@/config/config"
import { Pin } from "@/auth/pin"
// NOTE: Container execution handled by running swarm serve INSIDE container - no routing needed
import { isInWorkspaceDir } from "@/cli/cmd/tui/context/workspace"

const MAX_OUTPUT_LENGTH = 30_000
const DEFAULT_TIMEOUT = 1 * 60 * 1000
const MAX_TIMEOUT = 10 * 60 * 1000
const SIGKILL_TIMEOUT_MS = 200

export const log = Log.create({ service: "bash-tool" })

const parser = lazy(async () => {
  const { Parser } = await import("web-tree-sitter")
  const { default: treeWasm } = await import("web-tree-sitter/tree-sitter.wasm" as string, {
    with: { type: "wasm" },
  })
  await Parser.init({
    locateFile() {
      return treeWasm
    },
  })
  const { default: bashWasm } = await import("tree-sitter-bash/tree-sitter-bash.wasm" as string, {
    with: { type: "wasm" },
  })
  const bashLanguage = await Language.load(bashWasm)
  const p = new Parser()
  p.setLanguage(bashLanguage)
  return p
})

export const BashTool = Tool.define("bash", {
  description: DESCRIPTION,
  parameters: z.object({
    command: z.string().describe("The command to execute"),
    timeout: z.number().describe("Optional timeout in milliseconds").optional(),
    description: z
      .string()
      .describe(
        "Clear, concise description of what this command does in 5-10 words. Examples:\nInput: ls\nOutput: Lists files in current directory\n\nInput: git status\nOutput: Shows working tree status\n\nInput: npm install\nOutput: Installs package dependencies\n\nInput: mkdir foo\nOutput: Creates directory 'foo'",
      ),
  }),
  async execute(params, ctx) {
    if (params.timeout !== undefined && params.timeout < 0) {
      throw new Error(`Invalid timeout value: ${params.timeout}. Timeout must be a positive number.`)
    }

    // Check for obfuscation attempts in the command
    const sanitized = Sandbox.sanitize(params.command)
    if (sanitized.suspicious) {
      const hasBidi = sanitized.warnings.some((w) => w.includes("BiDi override"))
      if (hasBidi) {
        // BiDi overrides are extremely dangerous - text appears differently than it executes
        throw new Error(
          `SECURITY: Command contains BiDi override characters that make text display differently than it executes.\n` +
            `${sanitized.warnings.join("\n")}\n` +
            `Original appears as: ${params.command}\n` +
            `Actually executes as: ${sanitized.normalized}\n` +
            `This command has been blocked.`,
        )
      }
      // For other obfuscation (homoglyphs, zero-width), warn and use normalized
      log.warn("command contains obfuscated characters", {
        original: params.command,
        normalized: sanitized.normalized,
        warnings: sanitized.warnings,
      })
      // Replace command with normalized version
      params = { ...params, command: sanitized.normalized }
    }

    const timeout = Math.min(params.timeout ?? DEFAULT_TIMEOUT, MAX_TIMEOUT)
    const tree = await parser().then((p) => p.parse(params.command))
    if (!tree) {
      throw new Error("Failed to parse command")
    }
    const agent = await Agent.get(ctx.agent, { sessionID: ctx.sessionID })
    const permissions = agent.permission.bash

    const askPatterns = new Set<string>()
    const pinPatterns = new Set<string>()
    const externalPaths = new Set<string>()
    for (const node of tree.rootNode.descendantsOfType("command")) {
      if (!node) continue
      const command = []
      for (let i = 0; i < node.childCount; i++) {
        const child = node.child(i)
        if (!child) continue
        if (
          child.type !== "command_name" &&
          child.type !== "word" &&
          child.type !== "string" &&
          child.type !== "raw_string" &&
          child.type !== "concatenation"
        ) {
          continue
        }
        command.push(child.text)
      }

      // not an exhaustive list, but covers most common cases
      if (["cd", "rm", "cp", "mv", "mkdir", "touch", "chmod", "chown"].includes(command[0])) {
        for (const arg of command.slice(1)) {
          if (arg.startsWith("-") || (command[0] === "chmod" && arg.startsWith("+"))) continue
          const resolved = await $`realpath ${arg}`
            .quiet()
            .nothrow()
            .text()
            .then((x) => x.trim())
          log.info("resolved path", { arg, resolved })
          if (resolved && !Filesystem.contains(Instance.directory, resolved)) {
            const parentDir = await $`dirname ${resolved}`
              .quiet()
              .nothrow()
              .text()
              .then((x) => x.trim())
            if (parentDir) externalPaths.add(parentDir)
          }
        }
      }

      // always allow cd if it passes above check
      if (command[0] !== "cd") {
        const action = Wildcard.allStructured({ head: command[0], tail: command.slice(1) }, permissions)
        if (action === "deny") {
          throw new Error(
            `BLOCKED: This command is restricted by security configuration and cannot be executed.\n\n` +
              `Command: ${params.command}\n\n` +
              `What to do: Use the 'manual-command' tool to provide the user with a copy-able command they can run manually. ` +
              `Include a clear explanation of what the command does and any warnings about potential consequences.`,
          )
        }
        if (action === "pin") {
          // PIN-protected command
          const pattern = (() => {
            if (command.length === 0) return
            const head = command[0]
            const sub = command.slice(1).find((arg) => !arg.startsWith("-"))
            return sub ? `${head} ${sub} *` : `${head} *`
          })()
          if (pattern) {
            pinPatterns.add(pattern)
          }
        }
        if (action === "ask") {
          const pattern = (() => {
            if (command.length === 0) return
            const head = command[0]
            // Find first non-flag argument as subcommand
            const sub = command.slice(1).find((arg) => !arg.startsWith("-"))
            return sub ? `${head} ${sub} *` : `${head} *`
          })()
          if (pattern) {
            askPatterns.add(pattern)
          }
        }
      }
    }

    if (externalPaths.size > 0 && agent.permission.external_directory === "ask") {
      for (const parentDir of externalPaths) {
        // Check if path is in a workspace directory (auto-allow)
        const inWorkspace = await isInWorkspaceDir(parentDir)
        if (!inWorkspace) {
          await Permission.ask({
            type: "external_directory",
            pattern: parentDir,
            sessionID: ctx.sessionID,
            messageID: ctx.messageID,
            callID: ctx.callID,
            title: `Command accesses path outside working directory: ${parentDir}`,
            metadata: {
              command: params.command,
              parentDir,
            },
          })
        }
      }
    }

    if (askPatterns.size > 0) {
      const patterns = Array.from(askPatterns)
      await Permission.ask({
        type: "bash",
        pattern: patterns,
        sessionID: ctx.sessionID,
        messageID: ctx.messageID,
        callID: ctx.callID,
        title: params.command,
        metadata: {
          command: params.command,
          patterns,
        },
      })
    }

    // Handle PIN-protected commands
    if (pinPatterns.size > 0) {
      const hasPin = await Pin.exists()
      if (!hasPin) {
        throw new Error("This command requires PIN protection but no PIN is configured. Run: opencode pin set")
      }
      const patterns = Array.from(pinPatterns)
      await Permission.ask({
        type: "pin",
        pattern: patterns,
        sessionID: ctx.sessionID,
        messageID: ctx.messageID,
        callID: ctx.callID,
        title: `PIN required: ${params.command}`,
        metadata: {
          command: params.command,
          patterns,
        },
      })
    }

    // Check if command should bypass sandbox (trustedCommands)
    const config = await Config.get()
    const trustedCommands = config.sandbox?.trustedCommands ?? []
    const isTrustedCommand = trustedCommands.length > 0 && Wildcard.any(params.command, trustedCommands)

    // Wrap command with sandbox if enabled (unless it's a trusted command)
    Sandbox.setContext({
      sessionID: ctx.sessionID,
      messageID: ctx.messageID,
      callID: ctx.callID,
    })
    const commandToRun = isTrustedCommand
      ? params.command
      : await Sandbox.wrapCommand(params.command)

    // NOTE: Container execution is handled by running swarm serve INSIDE the container
    // No routing needed here - if swarm runs in container, commands naturally run there
    const proc = spawn(commandToRun, {
      shell: true,
      cwd: Instance.directory,
      env: {
        ...process.env,
      },
      stdio: ["ignore", "pipe", "pipe"],
      detached: process.platform !== "win32",
    })

    let output = ""

    // Initialize metadata with empty output
    ctx.metadata({
      metadata: {
        output: "",
        description: params.description,
      },
    })

    const append = (chunk: Buffer) => {
      output += chunk.toString()
      ctx.metadata({
        metadata: {
          output,
          description: params.description,
        },
      })
    }

    proc.stdout?.on("data", append)
    proc.stderr?.on("data", append)

    let timedOut = false
    let aborted = false
    let exited = false

    const killTree = async () => {
      const pid = proc.pid
      if (!pid || exited) {
        return
      }

      if (process.platform === "win32") {
        await new Promise<void>((resolve) => {
          const killer = spawn("taskkill", ["/pid", String(pid), "/f", "/t"], { stdio: "ignore" })
          killer.once("exit", resolve)
          killer.once("error", resolve)
        })
        return
      }

      try {
        process.kill(-pid, "SIGTERM")
        await Bun.sleep(SIGKILL_TIMEOUT_MS)
        if (!exited) {
          process.kill(-pid, "SIGKILL")
        }
      } catch (_e) {
        proc.kill("SIGTERM")
        await Bun.sleep(SIGKILL_TIMEOUT_MS)
        if (!exited) {
          proc.kill("SIGKILL")
        }
      }
    }

    if (ctx.abort.aborted) {
      aborted = true
      await killTree()
    }

    const abortHandler = () => {
      aborted = true
      void killTree()
    }

    ctx.abort.addEventListener("abort", abortHandler, { once: true })

    const timeoutTimer = setTimeout(() => {
      timedOut = true
      void killTree()
    }, timeout)

    await new Promise<void>((resolve, reject) => {
      const cleanup = () => {
        clearTimeout(timeoutTimer)
        ctx.abort.removeEventListener("abort", abortHandler)
      }

      proc.once("exit", () => {
        exited = true
        cleanup()
        resolve()
      })

      proc.once("error", (error) => {
        exited = true
        cleanup()
        reject(error)
      })
    })

    if (output.length > MAX_OUTPUT_LENGTH) {
      output = output.slice(0, MAX_OUTPUT_LENGTH)
      output += "\n\n(Output was truncated due to length limit)"
    }

    if (timedOut) {
      output += `\n\n(Command timed out after ${timeout} ms)`
    }

    if (aborted) {
      output += "\n\n(Command was aborted)"
    }

    // Annotate output with sandbox violation info if available
    output = Sandbox.annotateStderr(params.command, output)

    // Clear sandbox context
    Sandbox.clearContext()

    return {
      title: params.command,
      metadata: {
        output,
        exit: proc.exitCode,
        description: params.description,
      },
      output,
    }
  },
})
