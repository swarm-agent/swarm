import z from "zod"
import { Tool } from "./tool"
import { Sandbox } from "../sandbox"

export const ManualCommandTool = Tool.define("manual-command", {
  description:
    "Display commands that require manual execution by the user (sudo commands, blocked commands, system operations, etc). Use when you cannot execute a command directly but need the user to run it manually. The command will be displayed in a copy-able block.",
  parameters: z.object({
    command: z.string().describe("The command that needs to be run manually"),
    reason: z
      .string()
      .describe("Why this needs manual execution (e.g., 'requires sudo', 'blocked by security config', 'needs user confirmation')"),
  }),
  async execute(params) {
    // Sanitize the command to prevent text obfuscation attacks
    const sanitized = Sandbox.sanitize(params.command)

    // Block BiDi override characters - these make text display differently than it executes
    if (sanitized.suspicious) {
      const hasBidi = sanitized.warnings.some((w) => w.includes("BiDi override"))
      if (hasBidi) {
        throw new Error(
          `SECURITY: Command contains BiDi override characters that make text display differently than it executes.\n` +
            `Warnings: ${sanitized.warnings.join("; ")}\n` +
            `This command has been blocked for safety.`,
        )
      }
    }

    // Use the normalized (cleaned) command
    const cleanCommand = sanitized.normalized

    // Build output with any warnings
    let output = `Command: ${cleanCommand}\nReason: ${params.reason}`
    if (sanitized.warnings.length > 0 && !sanitized.warnings.some((w) => w.includes("BiDi"))) {
      output = `⚠️ Text cleaned: ${sanitized.warnings.join("; ")}\n\n${output}`
    }

    return {
      title: "Manual command",
      output,
      metadata: {
        command: cleanCommand, // Use cleaned version
        reason: params.reason,
        sanitized: sanitized.suspicious,
        warnings: sanitized.warnings,
      },
    }
  },
})
