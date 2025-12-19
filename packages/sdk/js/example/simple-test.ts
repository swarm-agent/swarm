#!/usr/bin/env bun
/**
 * Interactive test for SDK permission handling
 * 
 * Run: cd packages/sdk/js && bun run example/simple-test.ts
 */

import { createOpencode, type PermissionRequest, type PermissionResponse } from "../src/index.js"
import * as readline from "readline"

// Helper to prompt user for input
function prompt(question: string): Promise<string> {
  const rl = readline.createInterface({
    input: process.stdin,
    output: process.stdout,
  })
  
  return new Promise((resolve) => {
    rl.question(question, (answer) => {
      rl.close()
      resolve(answer.trim().toLowerCase())
    })
  })
}

async function main() {
  console.log("🚀 Starting SDK Interactive Permission Test\n")
  console.log("This test will STOP and ASK YOU for each permission!\n")
  console.log("Options when prompted:")
  console.log("  y / yes    - Approve once")
  console.log("  a / always - Approve and remember")
  console.log("  n / no     - Reject")
  console.log("")
  
  // Create opencode instance (starts server automatically)
  console.log("1. Creating opencode instance...")
  const { spawn, server } = await createOpencode()
  console.log(`   Server running at: ${server.url}\n`)

  try {
    console.log("2. Spawning agent in INTERACTIVE mode...")
    console.log("")
    
    const handle = spawn({
      prompt: `Create a file called /tmp/sdk-test.txt with the content "Hello from SDK test!" using the write tool.
Then read it back using the read tool and tell me what it says.`,
      mode: "interactive",
      agent: "build",
      
      // Permission callback - STOPS and waits for user input!
      onPermission: async (p: PermissionRequest): Promise<PermissionResponse> => {
        console.log("")
        console.log("╔═════════════════════════════════════════════════════════════")
        console.log("║ ⚠️  PERMISSION REQUEST - WAITING FOR YOUR INPUT")
        console.log("║")
        console.log(`║ Type:  ${p.permissionType}`)
        console.log(`║ Title: ${p.title}`)
        if (p.metadata.command) {
          console.log(`║ Command: ${p.metadata.command}`)
        }
        if (p.metadata.filePath) {
          console.log(`║ File: ${p.metadata.filePath}`)
        }
        console.log("║")
        console.log("╚═════════════════════════════════════════════════════════════")
        console.log("")
        
        // Actually wait for user input!
        const answer = await prompt(">>> Approve? (y)es / (a)lways / (n)o: ")
        
        switch (answer) {
          case "y":
          case "yes":
            console.log("   → Approved (once)\n")
            return "approve"
          case "a":
          case "always":
            console.log("   → Approved (always for this session)\n")
            return "always"
          case "n":
          case "no":
            console.log("   → Rejected\n")
            return "reject"
          default:
            console.log(`   → Unknown answer "${answer}", defaulting to approve\n`)
            return "approve"
        }
      },
    })

    console.log("3. Streaming events:\n")
    console.log("─".repeat(60))
    
    for await (const event of handle.stream()) {
      switch (event.type) {
        case "text":
          if (event.delta) {
            process.stdout.write(event.delta)
          }
          break
          
        case "tool.start":
          console.log(`\n\n📦 [TOOL] ${event.name}`)
          const inputStr = JSON.stringify(event.input, null, 2)
          if (inputStr.length < 300) {
            console.log(`   Input: ${inputStr}`)
          } else {
            console.log(`   Input: ${inputStr.slice(0, 300)}...`)
          }
          break
          
        case "tool.end":
          console.log(`   ✓ ${event.name} completed`)
          break
          
        case "permission":
          // Handled in onPermission callback - user is prompted there
          break
          
        case "permission.handled":
          console.log(`   ✓ Permission handled → ${JSON.stringify(event.response)}`)
          break
          
        case "todo":
          if (event.todos.length > 0) {
            console.log(`\n📋 [TODO] ${event.todos.length} items`)
            for (const todo of event.todos) {
              const icon = todo.status === "completed" ? "✅" : todo.status === "in_progress" ? "🔄" : "⬜"
              console.log(`   ${icon} ${todo.content}`)
            }
          }
          break
          
        case "completed":
          console.log("\n" + "─".repeat(60))
          console.log("\n🎉 Session completed successfully!")
          break
          
        case "aborted":
          console.log("\n" + "─".repeat(60))
          console.log("\n❌ Session was aborted")
          break
          
        case "error":
          console.log("\n" + "─".repeat(60))
          console.log(`\n💥 Error: ${event.error.message}`)
          break
      }
    }

  } catch (err) {
    console.error("\n❌ Test failed:", err)
  } finally {
    console.log("\n4. Closing server...")
    server.close()
    console.log("   Done!\n")
  }
}

main().catch(console.error)
