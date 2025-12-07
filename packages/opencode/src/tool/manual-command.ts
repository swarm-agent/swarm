import z from "zod"
import { Tool } from "./tool"

export const ManualCommandTool = Tool.define("manual-command", {
  description:
    "Display commands that require manual execution by the user (sudo commands, system operations, etc). Use when you cannot execute a command directly but need the user to run it manually. The command will be displayed in a copy-able block.",
  parameters: z.object({
    command: z.string().describe("The command that needs to be run manually"),
    reason: z.string().describe("Why this needs manual execution (e.g., 'requires sudo', 'needs user confirmation')"),
  }),
  async execute(params) {
    return {
      title: "Manual command",
      output: `Command: ${params.command}\nReason: ${params.reason}`,
      metadata: {
        command: params.command,
        reason: params.reason,
      },
    }
  },
})
