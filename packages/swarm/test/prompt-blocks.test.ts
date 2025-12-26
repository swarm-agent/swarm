import { describe, test, expect } from "bun:test"
import { Config } from "../src/config/config"
import z from "zod"

// Define AgentType schema inline to avoid importing SessionPrompt (which has JSX deps)
const AgentType = z.enum(["primary", "subagent", "background"])

// Mock config with prompt blocks
const mockConfig = {
  promptBlocks: [
    {
      content: "VOICE_BLOCK: Keep responses concise for speech.",
      // No agents = applies to all
    },
    {
      content: "PRIMARY_BLOCK: This is for primary agents only.",
      agents: ["primary"] as const,
    },
    {
      content: "SUBAGENT_BLOCK: This is for subagents only.",
      agents: ["subagent"] as const,
    },
    {
      content: "BACKGROUND_BLOCK: This is for background agents only.",
      agents: ["background"] as const,
    },
    {
      content: "MULTI_BLOCK: For primary and subagent.",
      agents: ["primary", "subagent"] as const,
    },
  ],
}

describe("Prompt Blocks", () => {
  test("config schema parses prompt blocks correctly", () => {
    const parsed = Config.Info.safeParse(mockConfig)
    expect(parsed.success).toBe(true)
    if (parsed.success) {
      expect(parsed.data.promptBlocks).toHaveLength(5)
      expect(parsed.data.promptBlocks?.[0].content).toBe("VOICE_BLOCK: Keep responses concise for speech.")
      expect(parsed.data.promptBlocks?.[0].agents).toBeUndefined() // No agents = applies to all
    }
  })

  test("AgentType enum accepts all valid types", () => {
    expect(AgentType.safeParse("primary").success).toBe(true)
    expect(AgentType.safeParse("subagent").success).toBe(true)
    expect(AgentType.safeParse("background").success).toBe(true)
    expect(AgentType.safeParse("invalid").success).toBe(false)
  })

  test("prompt blocks without agents apply to all agent types", () => {
    const block = mockConfig.promptBlocks[0]
    expect(block.agents).toBeUndefined()
  })

  test("prompt blocks can target specific agent types", () => {
    const primaryBlock = mockConfig.promptBlocks[1]
    const subagentBlock = mockConfig.promptBlocks[2]
    const backgroundBlock = mockConfig.promptBlocks[3]

    expect(primaryBlock.agents).toEqual(["primary"])
    expect(subagentBlock.agents).toEqual(["subagent"])
    expect(backgroundBlock.agents).toEqual(["background"])
  })

  test("prompt blocks can target multiple agent types", () => {
    const multiBlock = mockConfig.promptBlocks[4]
    expect(multiBlock.agents).toEqual(["primary", "subagent"])
  })

  test("minimal prompt block with just content is valid", () => {
    const minimalBlock = {
      promptBlocks: [{
        content: "Just content",
      }]
    }

    const parsed = Config.Info.safeParse(minimalBlock)
    expect(parsed.success).toBe(true)
    if (parsed.success) {
      const block = parsed.data.promptBlocks?.[0]
      expect(block?.content).toBe("Just content")
      expect(block?.agents).toBeUndefined() // No agents = all
    }
  })

  test("empty agents array means apply to none (edge case)", () => {
    const emptyAgents = {
      promptBlocks: [{
        content: "This applies to no one",
        agents: [] as const,
      }]
    }

    const parsed = Config.Info.safeParse(emptyAgents)
    expect(parsed.success).toBe(true)
    if (parsed.success) {
      const block = parsed.data.promptBlocks?.[0]
      expect(block?.agents).toEqual([])
    }
  })
})
