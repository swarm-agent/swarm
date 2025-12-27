import { Config } from "../config/config"
import z from "zod"
import { Provider } from "../provider/provider"
import { generateObject, type ModelMessage } from "ai"
import PROMPT_GENERATE from "./generate.txt"
import PROMPT_EXPLORE from "./explore.txt"
import PROMPT_MEMORY from "./memory.txt"
import PROMPT_BUILD from "./build.txt"
import PROMPT_PLAN from "./plan.txt"
import PROMPT_AUTO from "./auto.txt"
import { SystemPrompt } from "../session/system"
import { Instance } from "../project/instance"
import { mergeDeep } from "remeda"
import { Session } from "../session"
import { Profile } from "../profile"
import * as fs from "node:fs/promises"
import * as path from "node:path"
import * as yaml from "yaml"

export namespace Agent {
  // Tool groups for easy preset configuration
  export const TOOL_GROUPS = {
    // Read-only tools - safe for exploration
    readonly: [
      "read",
      "glob",
      "grep",
      "list",
      "explore-tree",
      "webfetch",
      "websearch",
      "webcontents",
      "todoread",
    ],
    // Write tools - can modify files
    write: ["write", "edit"],
    // Execution tools - can run commands
    execute: ["bash", "manual-command"],
    // Agent tools - can spawn other agents
    agent: ["task", "background-agent"],
    // Planning tools
    planning: ["todowrite", "todoread", "exit-plan-mode"],
    // Interactive tools
    interactive: ["ask-user"],
    // Memory tools
    memory: ["memory"],
  } as const

  // Preset configurations combining tool groups with permissions
  export const PRESETS = {
    // YOLO mode - all tools enabled, all permissions allowed
    yolo: {
      tools: {} as Record<string, boolean>, // Empty = all enabled (default behavior)
      permission: {
        edit: "allow" as const,
        bash: { "*": "allow" as const },
        webfetch: "allow" as const,
        external_directory: "allow" as const,
      },
    },
    // Read-only mode - only exploration tools, no modifications
    readonly: {
      tools: {
        // Enable read-only tools
        read: true,
        glob: true,
        grep: true,
        list: true,
        "explore-tree": true,
        webfetch: true,
        websearch: true,
        webcontents: true,
        todoread: true,
        // Disable write/execute tools
        write: false,
        edit: false,
        bash: false,
        "manual-command": false,
        task: false,
        "background-agent": false,
        todowrite: false,
        "exit-plan-mode": false,
        memory: false,
      },
      permission: {
        edit: "deny" as const,
        bash: { "*": "deny" as const },
        webfetch: "allow" as const,
        external_directory: "allow" as const,
      },
    },
    // Read-write mode - can read and modify files, but limited bash
    readwrite: {
      tools: {
        // Enable read tools
        read: true,
        glob: true,
        grep: true,
        list: true,
        "explore-tree": true,
        webfetch: true,
        websearch: true,
        webcontents: true,
        todoread: true,
        todowrite: true,
        // Enable write tools
        write: true,
        edit: true,
        // Limited execution
        bash: true,
        "manual-command": false,
        // Disable agent spawning
        task: false,
        "background-agent": false,
        "exit-plan-mode": false,
        memory: false,
      },
      permission: {
        edit: "allow" as const,
        bash: {
          // Allow safe bash commands
          "ls*": "allow" as const,
          "cat*": "allow" as const,
          "head*": "allow" as const,
          "tail*": "allow" as const,
          "grep*": "allow" as const,
          "rg*": "allow" as const,
          "find*": "allow" as const,
          "tree*": "allow" as const,
          "wc*": "allow" as const,
          "git status*": "allow" as const,
          "git diff*": "allow" as const,
          "git log*": "allow" as const,
          "git show*": "allow" as const,
          // Block dangerous commands
          "rm -rf*": "deny" as const,
          "sudo*": "deny" as const,
          "*": "ask" as const,
        },
        webfetch: "allow" as const,
        external_directory: "ask" as const,
      },
    },
    // Default mode - standard tool set with ask permissions
    default: {
      tools: {} as Record<string, boolean>, // Empty = use defaults
      permission: {
        edit: "ask" as const,
        bash: { "*": "ask" as const },
        webfetch: "allow" as const,
        external_directory: "ask" as const,
      },
    },
  } as const

  export type ToolPreset = keyof typeof PRESETS

  export const Info = z
    .object({
      name: z.string(),
      description: z.string().optional(),
      mode: z.union([z.literal("subagent"), z.literal("primary"), z.literal("all")]),
      builtIn: z.boolean(),
      topP: z.number().optional(),
      temperature: z.number().optional(),
      color: z.string().optional(),
      permission: z.object({
        edit: Config.Permission,
        bash: z.record(z.string(), Config.Permission),
        webfetch: Config.Permission.optional(),
        doom_loop: Config.Permission.optional(),
        external_directory: Config.Permission.optional(),
      }),
      model: z
        .object({
          modelID: z.string(),
          providerID: z.string(),
        })
        .optional(),
      prompt: z.string().optional(),
      tools: z.record(z.string(), z.boolean()),
      options: z.record(z.string(), z.any()),
    })
    .meta({
      ref: "Agent",
    })
  export type Info = z.infer<typeof Info>

  const state = Instance.state(async () => {
    const cfg = await Config.get()
    const defaultTools = cfg.tools ?? {}
    const defaultPermission: Info["permission"] = {
      edit: "ask",
      bash: {
        "*": "ask",
      },
      webfetch: "allow",
      doom_loop: "ask",
      external_directory: "ask",
    }
    const agentPermission = mergeAgentPermissions(defaultPermission, cfg.permission ?? {})

    // Read-only permission for explore agent
    const explorePermission: Info["permission"] = {
      edit: "deny",
      bash: {
        "rm *": "deny",
        "sudo *": "deny",
        "chmod *": "deny",
        "chown *": "deny",
        "dd *": "deny",
        "git add *": "deny",
        "git commit *": "deny",
        "git push *": "deny",
        "git reset --hard *": "deny",
        "*": "allow",
      },
      webfetch: "allow",
      external_directory: "allow",
    }

    const planPermission = mergeAgentPermissions(
      {
        edit: "deny",
        bash: {
          "cut*": "allow",
          "diff*": "allow",
          "du*": "allow",
          "file *": "allow",
          "find * -delete*": "ask",
          "find * -exec*": "ask",
          "find * -fprint*": "ask",
          "find * -fls*": "ask",
          "find * -fprintf*": "ask",
          "find * -ok*": "ask",
          "find *": "allow",
          "git diff*": "allow",
          "git log*": "allow",
          "git show*": "allow",
          "git status*": "allow",
          "git branch": "allow",
          "git branch -v": "allow",
          "grep*": "allow",
          "head*": "allow",
          "less*": "allow",
          "ls*": "allow",
          "more*": "allow",
          "pwd*": "allow",
          "rg*": "allow",
          "sort --output=*": "ask",
          "sort -o *": "ask",
          "sort*": "allow",
          "stat*": "allow",
          "tail*": "allow",
          "tree -o *": "ask",
          "tree*": "allow",
          "uniq*": "allow",
          "wc*": "allow",
          "whereis*": "allow",
          "which*": "allow",
          "*": "ask",
        },
        webfetch: "allow",
      },
      cfg.permission ?? {},
    )

    // Memory agent permission - can write AGENTS.md and amend commits
    const memoryPermission: Info["permission"] = {
      edit: "allow", // Needed to write AGENTS.md
      bash: {
        // Allow git operations for memory updates
        "git add AGENTS.md": "allow",
        "git add ./AGENTS.md": "allow",
        "git commit --amend --no-edit": "allow",
        "git commit --amend -m *": "allow",
        "git diff*": "allow",
        "git log*": "allow",
        "git show*": "allow",
        "git status*": "allow",
        // Read-only commands
        "cat*": "allow",
        "head*": "allow",
        "tail*": "allow",
        "ls*": "allow",
        "find*": "allow",
        "tree*": "allow",
        "wc*": "allow",
        // Block everything else
        "*": "deny",
      },
      webfetch: "allow",
      external_directory: "allow",
    }

    const result: Record<string, Info> = {
      general: {
        name: "general",
        description:
          "General-purpose agent for researching complex questions, searching for code, and executing multi-step tasks. When you are searching for a keyword or file and are not confident that you will find the right match in the first few tries use this agent to perform the search for you.",
        color: "#f7768e",
        tools: {
          todoread: false,
          todowrite: false,
          ...defaultTools,
        },
        options: {},
        permission: agentPermission,
        mode: "subagent",
        builtIn: true,
      },
      explore: {
        name: "explore",
        description:
          "Codebase exploration specialist - rapidly explores directory structures, reads multiple files in parallel, and searches patterns. Use for understanding architecture, finding implementations, or gathering context across many files. Faster and more efficient than general for pure exploration tasks.",
        color: "#50fa7b",
        temperature: 0.2,
        prompt: PROMPT_EXPLORE,
        tools: {
          "explore-tree": true,
          read: true,
          grep: true,
          glob: true,
          list: true,
          bash: true,
          webfetch: true,
          // Disable modification tools
          edit: false,
          write: false,
          task: false,
          todowrite: false,
          todoread: false,
          "exit-plan-mode": false,
          "background-agent": false,
        },
        options: {},
        permission: explorePermission,
        mode: "subagent",
        builtIn: true,
      },
      memory: {
        name: "memory",
        description:
          "AGENTS.md maintenance agent - creates and updates the project's persistent memory file. Use after commits to keep documentation in sync with code changes.",
        color: "#f1fa8c", // Yellow for memory/documentation
        temperature: 0.3,
        prompt: PROMPT_MEMORY,
        // Use configured model or default to opus
        model: cfg.memory?.model
          ? Provider.parseModel(cfg.memory.model)
          : Provider.parseModel("anthropic/claude-opus-4-5"),
        tools: {
          "explore-tree": true,
          read: true,
          grep: true,
          glob: true,
          list: true,
          bash: true,
          write: true, // Can write AGENTS.md
          webfetch: true,
          // Disable tools that don't make sense for memory
          edit: false, // Use write for AGENTS.md
          task: false,
          todowrite: false,
          todoread: false,
          "exit-plan-mode": false,
          "background-agent": false,
        },
        options: {},
        permission: memoryPermission,
        mode: "subagent",
        builtIn: true,
      },
      build: {
        name: "build",
        description: "Full development mode with permission controls",
        color: "#7aa2f7",
        prompt: PROMPT_BUILD,
        tools: { ...defaultTools },
        options: {},
        permission: agentPermission,
        mode: "primary",
        builtIn: true,
      },
      plan: {
        name: "plan",
        description: "Analysis and planning mode - read-only exploration and planning",
        color: "#bb9af7",
        prompt: PROMPT_PLAN,
        options: {},
        permission: planPermission,
        tools: {
          ...defaultTools,
          edit: false,
          write: false,
        },
        mode: "primary",
        builtIn: true,
      },
      auto: {
        name: "auto",
        description: "Autonomous mode - auto-approves safe operations",
        color: "#ff9a4a",
        prompt: PROMPT_AUTO,
        tools: { ...defaultTools },
        options: {},
        permission: {
          edit: "allow",
          webfetch: "allow",
          external_directory: "allow",
          bash: {
            "rm -rf *": "deny",
            "rm -r *": "deny",
            "sudo *": "ask",
            "git push --force *": "deny",
            "git reset --hard *": "deny",
            "*": "allow",
          },
        },
        mode: "primary",
        builtIn: true,
      },
    }
    for (const [key, value] of Object.entries(cfg.agent ?? {})) {
      if (value.disable) {
        delete result[key]
        continue
      }
      let item = result[key]
      if (!item)
        item = result[key] = {
          name: key,
          mode: "all",
          permission: agentPermission,
          options: {},
          tools: {},
          builtIn: false,
        }
      const { name, model, prompt, tools, description, temperature, top_p, mode, permission, color, ...extra } = value
      item.options = {
        ...item.options,
        ...extra,
      }
      if (model) item.model = Provider.parseModel(model)
      if (prompt) item.prompt = prompt
      if (tools)
        item.tools = {
          ...item.tools,
          ...tools,
        }
      item.tools = {
        ...defaultTools,
        ...item.tools,
      }
      if (description) item.description = description
      if (temperature != undefined) item.temperature = temperature
      if (top_p != undefined) item.topP = top_p
      if (mode) item.mode = mode
      if (color) item.color = color
      // just here for consistency & to prevent it from being added as an option
      if (name) item.name = name

      if (permission ?? cfg.permission) {
        item.permission = mergeAgentPermissions(cfg.permission ?? {}, permission ?? {})
      }
    }
    return result
  })

  export async function get(agent: string, options?: { isBackground?: boolean; sessionID?: string }) {
    const agents = await state()
    let result = agents[agent]
    if (!result) return result

    // If running as background agent, merge background-specific permissions
    if (options?.isBackground) {
      const cfg = await Config.get()
      const backgroundConfig = cfg.backgroundAgent
      if (backgroundConfig?.permission) {
        result = {
          ...result,
          permission: mergeAgentPermissions(result.permission, backgroundConfig.permission),
        }
      }
    }

    // If sessionID provided, check for container profile permissions and tools
    if (options?.sessionID) {
      try {
        const session = await Session.get(options.sessionID)
        if (session?.containerProfile) {
          const profile = await Profile.get(session.containerProfile)
          if (profile?.config) {
            // Merge permissions from profile
            if (profile.config.permission) {
              result = {
                ...result,
                permission: mergeAgentPermissions(result.permission, profile.config.permission),
              }
            }
            // Merge tools from profile (profile tools override agent tools)
            if (profile.config.tools) {
              result = {
                ...result,
                tools: { ...result.tools, ...profile.config.tools },
              }
            }
          }
        }
      } catch {
        // Session not found or profile not found, continue with default permissions
      }
    }

    return result
  }

  export async function list() {
    return state().then((x) => Object.values(x))
  }

  export async function generate(input: { description: string }) {
    const defaultModel = await Provider.defaultModel()
    const model = await Provider.getModel(defaultModel.providerID, defaultModel.modelID)
    const system = SystemPrompt.header(defaultModel.providerID)
    system.push(PROMPT_GENERATE)
    const existing = await list()
    const result = await generateObject({
      temperature: 0.3,
      prompt: [
        ...system.map(
          (item): ModelMessage => ({
            role: "system",
            content: item,
          }),
        ),
        {
          role: "user",
          content: `Create an agent configuration based on this request: \"${input.description}\".\n\nIMPORTANT: The following identifiers already exist and must NOT be used: ${existing.map((i) => i.name).join(", ")}\n  Return ONLY the JSON object, no other text, do not wrap in backticks`,
        },
      ],
      model: model.language,
      schema: z.object({
        identifier: z.string(),
        whenToUse: z.string(),
        systemPrompt: z.string(),
      }),
    })
    return result.object
  }

  // Input schema for creating/updating agents
  export const CreateInput = z.object({
    name: z.string().min(1).regex(/^[a-z0-9-]+$/, "Name must be lowercase alphanumeric with dashes"),
    description: z.string().optional(),
    mode: z.enum(["subagent", "primary", "all"]).default("all"),
    color: z.string().regex(/^#[0-9a-fA-F]{6}$/).optional(),
    prompt: z.string().optional(),
    model: z.string().optional(),
    temperature: z.number().min(0).max(2).optional(),
    topP: z.number().min(0).max(1).optional(),
    // Tool preset for easy configuration (yolo, readonly, readwrite, default)
    // If specified, preset tools/permissions are applied first, then explicit overrides
    toolPreset: z.enum(["yolo", "readonly", "readwrite", "default"]).optional(),
    // Fine-grained tool control (merged on top of preset if both specified)
    tools: z.record(z.string(), z.boolean()).optional(),
    // Fine-grained permission control (merged on top of preset if both specified)
    permission: z.object({
      edit: Config.Permission.optional(),
      bash: z.union([Config.Permission, z.record(z.string(), Config.Permission)]).optional(),
      webfetch: Config.Permission.optional(),
      external_directory: Config.Permission.optional(),
    }).optional(),
  })
  export type CreateInput = z.infer<typeof CreateInput>

  export const UpdateInput = CreateInput.partial().omit({ name: true })
  export type UpdateInput = z.infer<typeof UpdateInput>

  // Get the agent config directory path
  async function getAgentDir(): Promise<string> {
    const cfg = await Config.get()
    // Use project config dir if available, otherwise global
    const configDir = cfg.configPath ? path.dirname(cfg.configPath) : path.join(process.env.HOME || "", ".config", "swarm")
    const agentDir = path.join(configDir, "agent")
    await fs.mkdir(agentDir, { recursive: true })
    return agentDir
  }

  // Apply preset and merge with explicit overrides
  function applyPreset(input: {
    toolPreset?: ToolPreset
    tools?: Record<string, boolean>
    permission?: CreateInput["permission"]
  }): { tools: Record<string, boolean>; permission: CreateInput["permission"] } {
    // Start with preset if specified
    const preset = input.toolPreset ? PRESETS[input.toolPreset] : undefined

    // Merge tools: preset first, then explicit overrides
    const tools: Record<string, boolean> = {
      ...(preset?.tools ?? {}),
      ...(input.tools ?? {}),
    }

    // Merge permissions: preset first, then explicit overrides
    let permission: CreateInput["permission"] = undefined
    if (preset?.permission || input.permission) {
      const presetPerm = preset?.permission ?? {}
      const inputPerm = input.permission ?? {}

      // Handle bash specially - if input has bash, it should override preset bash
      let bash: CreateInput["permission"]["bash"] = undefined
      if (inputPerm.bash !== undefined) {
        bash = inputPerm.bash
      } else if (presetPerm.bash !== undefined) {
        bash = presetPerm.bash as CreateInput["permission"]["bash"]
      }

      permission = {
        edit: inputPerm.edit ?? presetPerm.edit,
        bash,
        webfetch: inputPerm.webfetch ?? presetPerm.webfetch,
        external_directory: inputPerm.external_directory ?? presetPerm.external_directory,
      }
    }

    return { tools, permission }
  }

  // Create a new agent
  export async function create(input: CreateInput): Promise<Info> {
    const existing = await state()
    if (existing[input.name]) {
      throw new Error(`Agent "${input.name}" already exists`)
    }

    const agentDir = await getAgentDir()
    const agentPath = path.join(agentDir, `${input.name}.md`)

    // Apply preset and merge with explicit overrides
    const { tools, permission } = applyPreset(input)

    // Build frontmatter
    const frontmatter: Record<string, any> = {}
    if (input.description) frontmatter.description = input.description
    if (input.mode) frontmatter.mode = input.mode
    if (input.color) frontmatter.color = input.color
    if (input.model) frontmatter.model = input.model
    if (input.temperature !== undefined) frontmatter.temperature = input.temperature
    if (input.topP !== undefined) frontmatter.top_p = input.topP
    if (Object.keys(tools).length > 0) frontmatter.tools = tools
    if (permission) frontmatter.permission = permission

    // Build markdown content
    const content = `---
${yaml.stringify(frontmatter).trim()}
---

${input.prompt || ""}
`

    await fs.writeFile(agentPath, content, "utf-8")

    // Reload config to pick up new agent
    await reload()

    const agent = await get(input.name)
    if (!agent) throw new Error("Failed to create agent")
    return agent
  }

  // Update an existing agent (built-in agents can be overridden via config file)
  export async function update(name: string, input: UpdateInput): Promise<Info> {
    const existing = await get(name)
    if (!existing) {
      throw new Error(`Agent "${name}" not found`)
    }
    // Built-in agents CAN be modified - creates a config override file

    const agentDir = await getAgentDir()
    const agentPath = path.join(agentDir, `${name}.md`)

    // Apply preset if specified, then merge with explicit overrides
    const { tools: presetTools, permission: presetPermission } = applyPreset(input)

    // Merge with existing (preset/explicit overrides take precedence)
    const merged = {
      description: input.description ?? existing.description,
      mode: input.mode ?? existing.mode,
      color: input.color ?? existing.color,
      model: input.model,
      temperature: input.temperature ?? existing.temperature,
      top_p: input.topP ?? existing.topP,
      // If preset was applied, use preset tools merged with explicit; otherwise use explicit or existing
      tools: input.toolPreset
        ? { ...existing.tools, ...presetTools }
        : (Object.keys(presetTools).length > 0 ? presetTools : existing.tools),
      // If preset was applied, use preset permission merged; otherwise use explicit or existing
      permission: presetPermission ?? input.permission ?? existing.permission,
      prompt: input.prompt ?? existing.prompt,
    }

    // Build frontmatter
    const frontmatter: Record<string, any> = {}
    if (merged.description) frontmatter.description = merged.description
    if (merged.mode) frontmatter.mode = merged.mode
    if (merged.color) frontmatter.color = merged.color
    if (merged.model) frontmatter.model = merged.model
    if (merged.temperature !== undefined) frontmatter.temperature = merged.temperature
    if (merged.top_p !== undefined) frontmatter.top_p = merged.top_p
    if (merged.tools && Object.keys(merged.tools).length > 0) frontmatter.tools = merged.tools
    if (merged.permission) frontmatter.permission = merged.permission

    // Build markdown content
    const content = `---
${yaml.stringify(frontmatter).trim()}
---

${merged.prompt || ""}
`

    await fs.writeFile(agentPath, content, "utf-8")

    // Reload config to pick up changes
    await reload()

    const agent = await get(name)
    if (!agent) throw new Error("Failed to update agent")
    return agent
  }

  // Delete an agent (built-in agents can be disabled via config override)
  export async function remove(name: string): Promise<boolean> {
    const existing = await get(name)
    if (!existing) {
      throw new Error(`Agent "${name}" not found`)
    }
    
    // Prevent removing the last primary agent - always need at least "build"
    if (name === "build") {
      const agents = await state()
      const primaryAgents = Object.values(agents).filter(a => a.mode === "primary")
      if (primaryAgents.length <= 1) {
        throw new Error(`Cannot delete "build" agent - it's the last primary agent`)
      }
    }

    const agentDir = await getAgentDir()
    const agentPath = path.join(agentDir, `${name}.md`)

    // For built-in agents, create a disable override file
    if (existing.builtIn) {
      const disableContent = `---
disable: true
---
`
      await fs.writeFile(agentPath, disableContent, "utf-8")
      await reload()
      return true
    }

    try {
      await fs.unlink(agentPath)
    } catch (e) {
      // File might not exist if agent was defined in config
      console.warn(`Could not delete agent file: ${e}`)
    }

    // Reload config
    await reload()
    return true
  }

  // Reload agent configuration
  export async function reload(): Promise<void> {
    // Clear the instance state to force reload
    await Instance.dispose()
  }

  // Get available presets with their descriptions
  export function getPresets(): Array<{
    name: ToolPreset
    description: string
    toolGroups: string[]
  }> {
    return [
      {
        name: "yolo",
        description: "All tools enabled, all permissions allowed - no restrictions",
        toolGroups: ["readonly", "write", "execute", "agent", "planning", "interactive", "memory"],
      },
      {
        name: "readonly",
        description: "Read-only exploration - no file modifications or command execution",
        toolGroups: ["readonly"],
      },
      {
        name: "readwrite",
        description: "Can read and modify files, limited bash with safe commands allowed",
        toolGroups: ["readonly", "write"],
      },
      {
        name: "default",
        description: "Standard tool set with ask permissions for sensitive operations",
        toolGroups: ["readonly", "write", "execute", "agent", "planning", "interactive", "memory"],
      },
    ]
  }

  // Get tool groups for reference
  export function getToolGroups(): typeof TOOL_GROUPS {
    return TOOL_GROUPS
  }
}

function mergeAgentPermissions(basePermission: any, overridePermission: any): Agent.Info["permission"] {
  // If override bash is a simple string (like "deny" or "allow"), it completely replaces base bash permissions
  // This allows profiles to say "bash: deny" to block all bash commands
  const overrideBashIsSimple = typeof overridePermission?.bash === "string"

  if (typeof basePermission.bash === "string") {
    basePermission.bash = {
      "*": basePermission.bash,
    }
  }
  if (typeof overridePermission?.bash === "string") {
    overridePermission.bash = {
      "*": overridePermission.bash,
    }
  }

  const merged = mergeDeep(basePermission ?? {}, overridePermission ?? {}) as any

  let mergedBash
  if (overrideBashIsSimple && overridePermission?.bash) {
    // Simple string override completely replaces bash permissions
    mergedBash = overridePermission.bash
  } else if (merged.bash) {
    if (typeof merged.bash === "string") {
      mergedBash = {
        "*": merged.bash,
      }
    } else if (typeof merged.bash === "object") {
      mergedBash = mergeDeep(
        {
          "*": "allow",
        },
        merged.bash,
      )
    }
  }

  const result: Agent.Info["permission"] = {
    edit: merged.edit ?? "allow",
    webfetch: merged.webfetch ?? "allow",
    bash: mergedBash ?? { "*": "allow" },
    doom_loop: merged.doom_loop,
    external_directory: merged.external_directory,
  }

  return result
}
