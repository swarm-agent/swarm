import z from "zod"
import { Tool } from "./tool"
import DESCRIPTION from "./memory.txt"
import { Log } from "../util/log"
import { Instance } from "../project/instance"
import path from "path"
import fs from "fs/promises"

const log = Log.create({ service: "memory-tool" })

const AGENTS_MD_TEMPLATE = `# Project

Brief description of the project.

## Quick Reference

| Command | Description |
|---------|-------------|
| \`npm run build\` | Build the project |
| \`npm test\` | Run tests |

## Architecture

- Key directories and patterns

## Code Style

- Conventions and formatting rules

## Session Log

<!-- Auto-updated by memory tool -->
| Date | Summary |
|------|---------|

## Notes

<!-- Learnings, gotchas, important decisions -->
`

export const MemoryTool = Tool.define("memory", async () => {
  return {
    description: DESCRIPTION,
    parameters: z.object({
      section: z
        .enum(["notes", "architecture", "commands", "style", "session"])
        .describe("Which section of AGENTS.md to update"),
      content: z.string().describe("The content to add to the section"),
    }),
    async execute(params, ctx) {
      const agentsPath = path.join(Instance.worktree, "AGENTS.md")

      // Read existing AGENTS.md or create from template
      let content: string
      try {
        content = await fs.readFile(agentsPath, "utf-8")
      } catch (err) {
        // File doesn't exist, create from template
        log.info("creating new AGENTS.md")
        content = AGENTS_MD_TEMPLATE
      }

      const today = new Date().toISOString().split("T")[0]
      let updated = false
      let sectionName = ""

      switch (params.section) {
        case "notes": {
          sectionName = "Notes"
          // Find Notes section and append
          const notesMatch = content.match(/## Notes\n(<!-- [^>]+ -->\n)?/)
          if (notesMatch) {
            const insertPos = notesMatch.index! + notesMatch[0].length
            const entry = `- ${params.content}\n`
            content = content.slice(0, insertPos) + entry + content.slice(insertPos)
            updated = true
          }
          break
        }

        case "session": {
          sectionName = "Session Log"
          // Find Session Log table and add row
          const sessionMatch = content.match(/## Session Log\n(<!-- [^>]+ -->\n)?\| Date \| Summary \|\n\|[-]+\|[-]+\|\n/)
          if (sessionMatch) {
            const insertPos = sessionMatch.index! + sessionMatch[0].length
            const entry = `| ${today} | ${params.content} |\n`
            content = content.slice(0, insertPos) + entry + content.slice(insertPos)
            updated = true
          }
          break
        }

        case "architecture": {
          sectionName = "Architecture"
          // Find Architecture section and append
          const archMatch = content.match(/## Architecture\n+/)
          if (archMatch) {
            const insertPos = archMatch.index! + archMatch[0].length
            const entry = `- ${params.content}\n`
            content = content.slice(0, insertPos) + entry + content.slice(insertPos)
            updated = true
          }
          break
        }

        case "commands": {
          sectionName = "Quick Reference"
          // Find Quick Reference table - this one is trickier, just append note
          const cmdMatch = content.match(/## Quick Reference\n/)
          if (cmdMatch) {
            // Add as a note after the table
            const tableEnd = content.indexOf("\n\n", cmdMatch.index! + cmdMatch[0].length)
            if (tableEnd !== -1) {
              const entry = `\n> ${params.content}\n`
              content = content.slice(0, tableEnd) + entry + content.slice(tableEnd)
              updated = true
            }
          }
          break
        }

        case "style": {
          sectionName = "Code Style"
          // Find Code Style section and append
          const styleMatch = content.match(/## Code Style\n+/)
          if (styleMatch) {
            const insertPos = styleMatch.index! + styleMatch[0].length
            const entry = `- ${params.content}\n`
            content = content.slice(0, insertPos) + entry + content.slice(insertPos)
            updated = true
          }
          break
        }
      }

      if (!updated) {
        // Section not found, append to end
        content += `\n## ${sectionName}\n\n- ${params.content}\n`
      }

      // Write the updated content
      await fs.writeFile(agentsPath, content, "utf-8")

      log.info("updated AGENTS.md", { section: params.section, content: params.content })

      return {
        title: `memory → ${params.section}`,
        metadata: {
          section: params.section,
          content: params.content,
        },
        output: `Added to ${sectionName}: ${params.content}`,
      }
    },
  }
})
