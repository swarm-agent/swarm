import { test, expect, describe } from "bun:test"
import { Permission } from "../../src/permission"

describe("Permission.Response schema", () => {
  test("parses string literals", () => {
    expect(Permission.Response.parse("once")).toBe("once")
    expect(Permission.Response.parse("always")).toBe("always")
    expect(Permission.Response.parse("reject")).toBe("reject")
  })

  test("parses reject with message", () => {
    const result = Permission.Response.parse({ type: "reject", message: "User declined" })
    expect(result).toEqual({ type: "reject", message: "User declined" })
  })

  test("parses response with agent", () => {
    const result = Permission.Response.parse({ type: "once", agent: "explorer" })
    expect(result).toEqual({ type: "once", agent: "explorer" })
  })

  test("parses response with answers - single string values", () => {
    const input = {
      type: "once" as const,
      answers: {
        q1: "coding",
        q2: "detailed",
      },
    }
    const result = Permission.Response.parse(input)
    expect(result).toEqual(input)
  })

  test("parses response with answers - array values for multi-select", () => {
    const input = {
      type: "once" as const,
      answers: {
        q1: ["option1", "option2"],
        q2: "single-value",
      },
    }
    const result = Permission.Response.parse(input)
    expect(result).toEqual(input)
  })

  test("answers field is preserved and not stripped", () => {
    // This is the critical test - answers must not be stripped by schema validation
    const input = {
      type: "once" as const,
      answers: {
        q1: "coding",
        q2: "detailed",
        q3: "great",
      },
    }
    const result = Permission.Response.parse(input)

    // Verify it's an object with answers (not just the literal "once")
    expect(typeof result).toBe("object")
    expect(result).toHaveProperty("answers")

    if (typeof result === "object" && "answers" in result) {
      expect(result.answers).toEqual({
        q1: "coding",
        q2: "detailed",
        q3: "great",
      })
    }
  })

  test("empty answers object is valid", () => {
    const input = { type: "once" as const, answers: {} }
    const result = Permission.Response.parse(input)
    expect(result).toEqual(input)
  })

  test("response with only type once (no agent, no answers) is valid", () => {
    const result = Permission.Response.parse({ type: "once" })
    expect(result).toEqual({ type: "once" })
  })

  test("response with type always and agent is valid", () => {
    const result = Permission.Response.parse({ type: "always", agent: "git-committer" })
    expect(result).toEqual({ type: "always", agent: "git-committer" })
  })
})
