/**
 * Examples of using the spawn() API with interactive and non-interactive modes
 */
import { createOpencode, type PermissionRequest, type PermissionResponse } from "@opencode-ai/sdk"

// ============================================================================
// Example 1: Non-interactive mode (default) - auto-approves permissions
// ============================================================================
async function nonInteractiveSpawn() {
  const { spawn, server } = await createOpencode()

  try {
    // Non-interactive is the default - permissions are auto-approved
    // This is ideal for CI/CD and automation
    const result = await spawn({
      prompt: "run npm test",
      // mode: "noninteractive" is the default
    }).wait()
    
    console.log("Result:", result)
  } finally {
    server.close()
  }
}

// ============================================================================
// Example 2: Interactive mode with permission callback
// ============================================================================
async function interactiveSpawn() {
  const { spawn, server } = await createOpencode()

  try {
    const handle = spawn({
      prompt: "run the tests and fix any failures",
      mode: "interactive",
      
      onPermission: async (p: PermissionRequest): Promise<PermissionResponse> => {
        console.log("\n--- Permission Request ---")
        console.log(`Type: ${p.permissionType}`)
        console.log(`Title: ${p.title}`)
        console.log(`Metadata:`, p.metadata)
        
        // Auto-approve test commands
        if (p.title.includes("npm test") || p.title.includes("bun test")) {
          console.log("Auto-approving test command")
          return "approve"
        }
        
        // Reject dangerous commands
        if (p.title.includes("rm -rf") || p.title.includes("deploy")) {
          console.log("Rejecting dangerous command")
          return { reject: "Dangerous command not allowed in this context" }
        }
        
        // Approve everything else once
        return "approve"
      },
    })

    for await (const event of handle.stream()) {
      switch (event.type) {
        case "text":
          if (event.delta) process.stdout.write(event.delta)
          break
        case "tool.start":
          console.log(`\n[Tool] ${event.name} starting...`)
          break
        case "tool.end":
          console.log(`[Tool] ${event.name} completed`)
          break
        case "permission":
          console.log(`[Permission Request] ${event.title}`)
          break
        case "permission.handled":
          console.log(`[Permission Handled] ${event.id} -> ${JSON.stringify(event.response)}`)
          break
      }
    }
  } finally {
    server.close()
  }
}

// ============================================================================
// Example 3: Interactive mode with PIN support
// ============================================================================
async function interactiveWithPin() {
  const { spawn, server } = await createOpencode()

  try {
    const handle = spawn({
      prompt: "delete the old backup files",
      mode: "interactive",
      
      onPermission: async (p: PermissionRequest): Promise<PermissionResponse> => {
        console.log(`Permission: ${p.permissionType} - ${p.title}`)
        
        // If PIN is required, get it from environment
        if (p.permissionType === "pin") {
          const pin = process.env.SWARM_PIN
          if (!pin) {
            return { reject: "PIN required but SWARM_PIN not set" }
          }
          return { pin }
        }
        
        return "approve"
      },
    })

    const result = await handle.wait()
    console.log("Result:", result)
  } finally {
    server.close()
  }
}

// ============================================================================
// Example 4: Non-interactive with tool restrictions
// ============================================================================
async function nonInteractiveWithRestrictions() {
  const { spawn, server } = await createOpencode()

  try {
    // Non-interactive but with explicit tool restrictions
    // Disabled tools won't even be attempted
    const result = await spawn({
      prompt: "analyze the codebase",
      mode: "noninteractive",
      tools: {
        bash: false,    // Disable bash entirely
        edit: false,    // Disable edits
        write: false,   // Disable writes
        // read, glob, grep remain enabled
      },
    }).wait()
    
    console.log("Result:", result)
  } finally {
    server.close()
  }
}

// ============================================================================
// Example 5: Using profiles with modes
// ============================================================================
async function profilesWithModes() {
  const { spawn, server } = await createOpencode()

  try {
    // 'yolo' profile forces noninteractive mode
    await spawn({
      prompt: "run all the tests",
      profile: "yolo",  // Forces noninteractive, all tools enabled
    }).wait()

    // 'full' profile with interactive mode
    await spawn({
      prompt: "deploy to staging",
      profile: "full",
      mode: "interactive",
      onPermission: async (p) => {
        // Only approve after logging
        console.log(`Approving: ${p.title}`)
        return "approve"
      },
    }).wait()

    // 'analyze' profile - read-only, no need for permissions
    await spawn({
      prompt: "explain the auth flow",
      profile: "analyze",  // No modification tools, so no permissions needed
    }).wait()
  } finally {
    server.close()
  }
}

// ============================================================================
// Example 6: Streaming with permission events
// ============================================================================
async function streamingWithPermissions() {
  const { spawn, server } = await createOpencode()

  try {
    const handle = spawn({
      prompt: "set up the development environment",
      mode: "interactive",
      onPermission: async (p) => {
        // Simulate async decision (e.g., calling external approval service)
        await new Promise(resolve => setTimeout(resolve, 100))
        return "approve"
      },
    })

    let permissionCount = 0
    let toolCount = 0

    for await (const event of handle.stream()) {
      switch (event.type) {
        case "text":
          if (event.delta) process.stdout.write(event.delta)
          break
        case "tool.start":
          toolCount++
          console.log(`\n[Tool #${toolCount}] ${event.name}`)
          break
        case "permission":
          permissionCount++
          console.log(`\n[Permission #${permissionCount}] ${event.title}`)
          break
        case "permission.handled":
          console.log(`[Permission #${permissionCount} handled] -> ${event.response}`)
          break
        case "completed":
          console.log(`\n\nCompleted! Tools used: ${toolCount}, Permissions handled: ${permissionCount}`)
          break
      }
    }
  } finally {
    server.close()
  }
}

// ============================================================================
// Example 7: Using specific agents
// ============================================================================
async function agentExamples() {
  const { spawn, server } = await createOpencode()

  try {
    // Plan agent - read-only by default
    await spawn({
      prompt: "analyze how to add dark mode",
      agent: "plan",
      mode: "noninteractive",
    }).wait()

    // Build agent with custom tool overrides
    await spawn({
      prompt: "implement the toggle",
      agent: "build",
      tools: { bash: true },  // Explicitly enable bash
      mode: "interactive",
      onPermission: async (p) => {
        console.log(`Build agent permission: ${p.title}`)
        return "approve"
      },
    }).wait()
  } finally {
    server.close()
  }
}

// ============================================================================
// Example 8: Fire and forget with completion callback
// ============================================================================
async function fireAndForget() {
  const { spawn, server } = await createOpencode()

  // Track completion
  let completed = 0
  const total = 3

  function checkDone() {
    completed++
    if (completed === total) {
      console.log("All tasks completed!")
      server.close()
    }
  }

  // Spawn multiple non-interactive tasks
  spawn({
    prompt: "fix lint errors in src/auth",
    mode: "noninteractive",
    onComplete: (result) => {
      console.log("Auth fixes:", result.success ? "done" : "failed")
      checkDone()
    },
  })

  spawn({
    prompt: "add missing types in src/utils",
    mode: "noninteractive", 
    onComplete: (result) => {
      console.log("Utils types:", result.success ? "done" : "failed")
      checkDone()
    },
  })

  spawn({
    prompt: "update README with new API",
    mode: "noninteractive",
    tools: { bash: false },  // No bash needed for docs
    onComplete: (result) => {
      console.log("README update:", result.success ? "done" : "failed")
      checkDone()
    },
  })

  // Don't close server here - let onComplete handlers do it
}

// ============================================================================
// Run examples
// ============================================================================
const example = process.argv[2] || "noninteractive"

const examples: Record<string, () => Promise<void>> = {
  "noninteractive": nonInteractiveSpawn,
  "interactive": interactiveSpawn,
  "pin": interactiveWithPin,
  "restricted": nonInteractiveWithRestrictions,
  "profiles": profilesWithModes,
  "streaming": streamingWithPermissions,
  "agents": agentExamples,
  "fire-forget": fireAndForget,
}

const fn = examples[example]
if (fn) {
  await fn()
} else {
  console.log("Usage: bun run example/spawn-examples.ts [example]")
  console.log("Examples:", Object.keys(examples).join(", "))
}
