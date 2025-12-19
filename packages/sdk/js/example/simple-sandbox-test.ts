/**
 * Comprehensive SDK Sandbox Test
 * 
 * Tests:
 * 1. Sandbox DISABLED - agent CAN write anywhere
 * 2. Sandbox ENABLED - agent CANNOT write to denied paths
 * 3. Sandbox ENABLED - agent CAN write to allowed paths
 * 4. Network: allowed domains work, denied domains blocked
 */

import { createOpencode } from "../src"
import * as fs from "fs"

const ALLOWED_DIR = "/tmp/sandbox-test-allowed"
const DENIED_DIR = "/tmp/sandbox-test-denied"
const PROTECTED_FILE = `${DENIED_DIR}/protected.txt`
const ALLOWED_FILE = `${ALLOWED_DIR}/allowed.txt`
const ORIGINAL_CONTENT = "original"

async function setup() {
  fs.rmSync(ALLOWED_DIR, { recursive: true, force: true })
  fs.rmSync(DENIED_DIR, { recursive: true, force: true })
  fs.mkdirSync(ALLOWED_DIR, { recursive: true })
  fs.mkdirSync(DENIED_DIR, { recursive: true })
  fs.writeFileSync(PROTECTED_FILE, ORIGINAL_CONTENT)
  try { fs.unlinkSync(ALLOWED_FILE) } catch {}
}

async function waitForAgent(client: any, sessionId: string, prompt: string, seconds: number = 15) {
  await client.session.prompt({
    path: { id: sessionId },
    body: { parts: [{ type: "text", text: prompt }] }
  })
  await new Promise(r => setTimeout(r, seconds * 1000))
}

// Test 1: Sandbox DISABLED - writes should succeed
async function testDisabled(): Promise<boolean> {
  console.log("\n" + "=".repeat(50))
  console.log("TEST 1: Sandbox DISABLED - writes should SUCCEED")
  console.log("=".repeat(50))
  
  await setup()
  
  const { client, server } = await createOpencode({
    config: {
      sandbox: { enabled: false }
    }
  })
  
  await new Promise(r => setTimeout(r, 2000))
  const session = await client.session.create()
  const id = session.data?.id!
  console.log(`Session: ${id}`)

  await waitForAgent(client, id, `Run: echo "HACKED" > ${PROTECTED_FILE}`)
  
  const content = fs.readFileSync(PROTECTED_FILE, "utf-8").trim()
  server.close()
  
  const passed = content === "HACKED"
  console.log(`File content: "${content}"`)
  console.log(passed ? "✅ PASS - Write succeeded (sandbox disabled)" : "❌ FAIL - Write blocked but sandbox was disabled!")
  return passed
}

// Test 2: Sandbox ENABLED - writes to denied path should FAIL
async function testEnabledDenied(): Promise<boolean> {
  console.log("\n" + "=".repeat(50))
  console.log("TEST 2: Sandbox ENABLED - denied writes should FAIL")
  console.log("=".repeat(50))
  
  await setup()
  
  const { client, server } = await createOpencode({
    config: {
      sandbox: {
        enabled: true,
        filesystem: {
          allowWrite: [ALLOWED_DIR, "/tmp", "~/.local/share/opencode", "~/.config/opencode", "."],
          denyWrite: [DENIED_DIR]
        }
      }
    }
  })
  
  await new Promise(r => setTimeout(r, 2000))
  const session = await client.session.create()
  const id = session.data?.id!
  console.log(`Session: ${id}`)

  await waitForAgent(client, id, `Run: echo "HACKED" > ${PROTECTED_FILE}`)
  
  const content = fs.readFileSync(PROTECTED_FILE, "utf-8").trim()
  server.close()
  
  const passed = content === ORIGINAL_CONTENT
  console.log(`File content: "${content}"`)
  console.log(passed ? "✅ PASS - Write blocked (sandbox working)" : "❌ FAIL - Write succeeded but should be blocked!")
  return passed
}

// Test 3: Sandbox ENABLED - writes to allowed path should SUCCEED
async function testEnabledAllowed(): Promise<boolean> {
  console.log("\n" + "=".repeat(50))
  console.log("TEST 3: Sandbox ENABLED - allowed writes should SUCCEED")
  console.log("=".repeat(50))
  
  await setup()
  
  const { client, server } = await createOpencode({
    config: {
      sandbox: {
        enabled: true,
        filesystem: {
          allowWrite: [ALLOWED_DIR, "/tmp", "~/.local/share/opencode", "~/.config/opencode", "."],
          denyWrite: [DENIED_DIR]
        }
      }
    }
  })
  
  await new Promise(r => setTimeout(r, 2000))
  const session = await client.session.create()
  const id = session.data?.id!
  console.log(`Session: ${id}`)

  await waitForAgent(client, id, `Run: echo "ALLOWED" > ${ALLOWED_FILE}`)
  
  const exists = fs.existsSync(ALLOWED_FILE)
  const content = exists ? fs.readFileSync(ALLOWED_FILE, "utf-8").trim() : ""
  server.close()
  
  const passed = content === "ALLOWED"
  console.log(`File exists: ${exists}, content: "${content}"`)
  console.log(passed ? "✅ PASS - Write to allowed path succeeded" : "❌ FAIL - Write to allowed path blocked!")
  return passed
}

// Test 4: Network - allowed domain should work
async function testNetworkAllowed(): Promise<boolean> {
  console.log("\n" + "=".repeat(50))
  console.log("TEST 4: Network - allowed domain should WORK")
  console.log("=".repeat(50))
  
  const { client, server } = await createOpencode({
    config: {
      sandbox: {
        enabled: true,
        network: {
          allowedDomains: ["example.com", "*.example.com"]
        },
        filesystem: {
          allowWrite: ["/tmp", "~/.local/share/opencode", "~/.config/opencode", "."]
        }
      }
    }
  })
  
  await new Promise(r => setTimeout(r, 2000))
  const session = await client.session.create()
  const id = session.data?.id!
  console.log(`Session: ${id}`)

  await waitForAgent(client, id, `Run: curl -s -o /tmp/curl-test.txt -w "%{http_code}" https://example.com && cat /tmp/curl-test.txt | head -1`)
  
  const exists = fs.existsSync("/tmp/curl-test.txt")
  const content = exists ? fs.readFileSync("/tmp/curl-test.txt", "utf-8").substring(0, 100) : ""
  server.close()
  
  const passed = content.includes("Example") || content.includes("DOCTYPE")
  console.log(`Response received: ${exists}, preview: "${content.substring(0, 50)}..."`)
  console.log(passed ? "✅ PASS - Allowed domain accessible" : "❌ FAIL - Allowed domain blocked!")
  return passed
}

// Test 5: Network - denied domain should be blocked
async function testNetworkDenied(): Promise<boolean> {
  console.log("\n" + "=".repeat(50))
  console.log("TEST 5: Network - non-allowed domain should be BLOCKED")
  console.log("=".repeat(50))
  
  try { fs.unlinkSync("/tmp/curl-denied-test.txt") } catch {}
  
  const { client, server } = await createOpencode({
    config: {
      sandbox: {
        enabled: true,
        network: {
          allowedDomains: ["example.com"]  // Only example.com, NOT google
        },
        filesystem: {
          allowWrite: ["/tmp", "~/.local/share/opencode", "~/.config/opencode", "."]
        }
      }
    }
  })
  
  await new Promise(r => setTimeout(r, 2000))
  const session = await client.session.create()
  const id = session.data?.id!
  console.log(`Session: ${id}`)

  await waitForAgent(client, id, `Run: curl -s --max-time 5 https://google.com > /tmp/curl-denied-test.txt 2>&1 || echo "BLOCKED" > /tmp/curl-denied-test.txt`)
  
  const exists = fs.existsSync("/tmp/curl-denied-test.txt")
  const content = exists ? fs.readFileSync("/tmp/curl-denied-test.txt", "utf-8").trim() : "BLOCKED"
  server.close()
  
  // Should NOT contain google content
  const passed = !content.includes("google") && !content.includes("Google") && !content.includes("<!doctype")
  console.log(`Response: "${content.substring(0, 50)}..."`)
  console.log(passed ? "✅ PASS - Non-allowed domain blocked" : "❌ FAIL - Non-allowed domain was accessible!")
  return passed
}

async function main() {
  console.log("🧪 SDK SANDBOX COMPREHENSIVE TEST")
  console.log("==================================\n")
  
  const results: { name: string; passed: boolean }[] = []
  
  results.push({ name: "Sandbox DISABLED", passed: await testDisabled() })
  await new Promise(r => setTimeout(r, 2000))
  
  results.push({ name: "Sandbox ENABLED (deny)", passed: await testEnabledDenied() })
  await new Promise(r => setTimeout(r, 2000))
  
  results.push({ name: "Sandbox ENABLED (allow)", passed: await testEnabledAllowed() })
  await new Promise(r => setTimeout(r, 2000))
  
  results.push({ name: "Network (allowed)", passed: await testNetworkAllowed() })
  await new Promise(r => setTimeout(r, 2000))
  
  results.push({ name: "Network (denied)", passed: await testNetworkDenied() })

  console.log("\n" + "=".repeat(50))
  console.log("FINAL RESULTS")
  console.log("=".repeat(50))
  
  for (const r of results) {
    console.log(`${r.passed ? "✅" : "❌"} ${r.name}`)
  }
  
  const allPassed = results.every(r => r.passed)
  console.log(`\n${allPassed ? "🎉 ALL TESTS PASSED!" : "💥 SOME TESTS FAILED"}`)
  
  process.exit(allPassed ? 0 : 1)
}

main().catch(e => { console.error(e); process.exit(1) })
