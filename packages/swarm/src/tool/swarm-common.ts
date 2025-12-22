import { Auth } from "../auth"

/**
 * SwarmAgent API base URL
 */
export const SWARM_API_BASE = "https://swarmagent.dev/api"

/**
 * Get the SwarmAgent API key from auth.json or environment variable
 * Returns undefined if not configured (tools will be disabled via registry)
 */
export async function getSwarmApiKey(): Promise<string | undefined> {
  // Check auth.json first (preferred)
  const authInfo = await Auth.get("swarmagent")
  if (authInfo?.type === "api" && authInfo.key) {
    return authInfo.key
  }
  // Fallback to environment variable
  return process.env.SWARM_AGENT_API_KEY
}

/**
 * Check if SwarmAgent API key is configured (used by registry for opt-in)
 */
export async function isSwarmConfigured(): Promise<boolean> {
  const key = await getSwarmApiKey()
  return !!key
}

/**
 * Validate SwarmAgent API key by making a test request
 * Returns true if key is valid, false otherwise
 */
export async function validateSwarmApiKey(apiKey: string): Promise<boolean> {
  try {
    const response = await fetch(`${SWARM_API_BASE}/theme`, {
      method: "GET",
      headers: {
        "X-API-Key": apiKey,
      },
      signal: AbortSignal.timeout(10000),
    })
    return response.ok
  } catch {
    return false
  }
}
