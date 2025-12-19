/**
 * Test script for container-based agent spawning
 *
 * Usage:
 *   bun run packages/sdk/js/example/container-test.ts
 */

import {
  spawnContainer,
  listContainers,
  stopContainer,
  sendMessage,
  type ContainerHandle
} from "../src/container.js"

async function main() {
  console.log("=== Container SDK Test ===\n")

  // Clean up any existing test containers
  console.log("Cleaning up old containers...")
  try {
    stopContainer("sdk-test")
  } catch {}

  // List existing containers
  console.log("\nExisting swarm containers:")
  const existing = listContainers()
  if (existing.length === 0) {
    console.log("  (none)")
  } else {
    for (const c of existing) {
      console.log(`  ${c.name} - ${c.status} - port ${c.port}`)
    }
  }

  // Spawn a new container
  console.log("\n--- Spawning container ---")
  let container: ContainerHandle

  try {
    container = await spawnContainer({
      name: "sdk-test",
      workspace: "process.cwd()",
      // port: 0 means auto-assign
    })
  } catch (e) {
    console.error("Failed to spawn container:", e)
    process.exit(1)
  }

  console.log(`Container spawned!`)
  console.log(`  Name: ${container.name}`)
  console.log(`  ID: ${container.id.slice(0, 12)}...`)
  console.log(`  URL: ${container.url}`)
  console.log(`  Port: ${container.port}`)
  console.log(`  Running: ${container.isRunning()}`)

  // Create a session
  console.log("\n--- Creating session ---")
  const sessionResponse = await container.client.session.create({})
  const session = sessionResponse.data
  if (!session) {
    console.error("Failed to create session:", sessionResponse.error)
    await container.stop()
    process.exit(1)
  }
  console.log(`Session: ${session.id}`)
  console.log(`Directory: ${session.directory}`)

  // Send a message with auto-approval
  console.log("\n--- Sending message ---")
  console.log("Prompt: What is your current working directory and what OS are you running on?")
  console.log("\n--- Response ---")

  const result = await sendMessage(
    container.url,
    session.id,
    "What is your current working directory and what OS are you running on? Run pwd and cat /etc/os-release",
    {
      autoApprove: true,
      onText: (text) => {
        // Clear and reprint for streaming effect
        process.stdout.write("\r\x1b[K" + text.slice(-80))
      },
    }
  )

  console.log("\n")
  console.log("Full response:")
  console.log(result.text)
  console.log(`\nSuccess: ${result.success}`)

  // Clean up
  console.log("\n--- Cleanup ---")
  await container.stop()
  console.log("Container stopped")

  // Verify it's gone
  const after = listContainers()
  const stillExists = after.find(c => c.name === container.name)
  console.log(`Container removed: ${!stillExists}`)
}

main().catch(console.error)
