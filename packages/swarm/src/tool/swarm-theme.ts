import z from "zod"
import { Tool } from "./tool"
import DESCRIPTION from "./swarm-theme.txt"
import { getSwarmApiKey, SWARM_API_BASE } from "./swarm-common"

/**
 * Theme preset names
 */
const THEME_PRESETS = [
  "dark",
  "midnight",
  "cyberpunk",
  "matrix",
  "dracula",
  "nord",
  "monokai",
  "solarized",
] as const

type ThemePreset = (typeof THEME_PRESETS)[number]

/**
 * Theme configuration structure
 */
interface ThemeConfig {
  colors?: {
    primary?: string
    background?: string
    textPrimary?: string
    [key: string]: string | undefined
  }
  effects?: {
    glowEnabled?: boolean
    glowIntensity?: string
    animationsEnabled?: boolean
  }
  fonts?: {
    sans?: string
    mono?: string
  }
}

export const SwarmThemeTool: Tool.Info = Tool.define("swarm-theme", {
  description: DESCRIPTION,
  parameters: z.object({
    action: z.enum(["get", "preset"]).describe("Action to perform: get or preset"),
    preset: z
      .enum(THEME_PRESETS)
      .optional()
      .describe("Theme preset to apply (required for preset action)"),
  }),

  async execute(params, ctx) {
    const apiKey = await getSwarmApiKey()
    if (!apiKey) {
      throw new Error(
        "SwarmAgent API key not configured.\n" +
          "Run: swarm auth login → Other → swarmagent → paste key\n" +
          "Or set SWARM_AGENT_API_KEY environment variable.\n" +
          "Get your key at: https://swarmagent.dev/dashboard → API Keys"
      )
    }

    switch (params.action) {
      case "get": {
        const response = await fetch(`${SWARM_API_BASE}/theme`, {
          method: "GET",
          headers: {
            "X-API-Key": apiKey,
          },
          signal: ctx.abort,
        })

        if (!response.ok) {
          const error = await response.text()
          throw new Error(`Failed to get theme (${response.status}): ${error}`)
        }

        const theme: ThemeConfig = await response.json()

        const output = [
          "Current Theme Configuration:",
          "",
          "Colors:",
          `  Primary: ${theme.colors?.primary || "default"}`,
          `  Background: ${theme.colors?.background || "default"}`,
          `  Text: ${theme.colors?.textPrimary || "default"}`,
          "",
          "Effects:",
          `  Glow: ${theme.effects?.glowEnabled ? "enabled" : "disabled"}`,
          `  Glow Intensity: ${theme.effects?.glowIntensity || "medium"}`,
          `  Animations: ${theme.effects?.animationsEnabled ? "enabled" : "disabled"}`,
          "",
          "Fonts:",
          `  Sans: ${theme.fonts?.sans || "Inter"}`,
          `  Mono: ${theme.fonts?.mono || "JetBrains Mono"}`,
        ].join("\n")

        return {
          title: "Current theme",
          output,
          metadata: {
            theme,
          },
        }
      }

      case "preset": {
        if (!params.preset) {
          throw new Error("preset is required for preset action")
        }

        const response = await fetch(
          `${SWARM_API_BASE}/theme/preset/${params.preset}`,
          {
            method: "POST",
            headers: {
              "X-API-Key": apiKey,
            },
            signal: ctx.abort,
          }
        )

        if (!response.ok) {
          const error = await response.text()
          throw new Error(`Failed to apply theme (${response.status}): ${error}`)
        }

        const presetDescriptions: Record<ThemePreset, string> = {
          dark: "Default dark theme with green accents",
          midnight: "Deep blue professional theme",
          cyberpunk: "Neon cyan and magenta futuristic theme",
          matrix: "Classic green terminal aesthetic",
          dracula: "Purple and pink dark theme",
          nord: "Cool blue Scandinavian design",
          monokai: "Classic code editor color scheme",
          solarized: "Warm professional solarized dark",
        }

        return {
          title: `Applied: ${params.preset}`,
          output: `🎨 Theme "${params.preset}" applied successfully!\n\n${presetDescriptions[params.preset]}`,
          metadata: {
            preset: params.preset,
          },
        }
      }

      default:
        throw new Error(`Unknown action: ${params.action}`)
    }
  },
})
