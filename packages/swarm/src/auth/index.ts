import path from "path"
import os from "os"
import { Global } from "../global"
import fs from "fs/promises"
import z from "zod"

export namespace Auth {
  export const Oauth = z
    .object({
      type: z.literal("oauth"),
      refresh: z.string(),
      access: z.string(),
      expires: z.number(),
      enterpriseUrl: z.string().optional(),
    })
    .meta({ ref: "OAuth" })

  export const Api = z
    .object({
      type: z.literal("api"),
      key: z.string(),
    })
    .meta({ ref: "ApiAuth" })

  export const WellKnown = z
    .object({
      type: z.literal("wellknown"),
      key: z.string(),
      token: z.string(),
    })
    .meta({ ref: "WellKnownAuth" })

  export const Info = z.discriminatedUnion("type", [Oauth, Api, WellKnown]).meta({ ref: "Auth" })
  export type Info = z.infer<typeof Info>

  const filepath = path.join(Global.Path.data, "auth.json")
  // Backwards compat: check legacy opencode location
  const legacyFilepath = path.join(os.homedir(), ".local", "share", "opencode", "auth.json")

  export async function get(providerID: string) {
    const data = await all()
    return data[providerID] as Info | undefined
  }

  export async function all(): Promise<Record<string, Info>> {
    const file = Bun.file(filepath)
    const legacyFile = Bun.file(legacyFilepath)
    const [current, legacy] = await Promise.all([
      file.json().catch(() => ({})),
      legacyFile.json().catch(() => ({})),
    ])
    // Merge: current takes precedence over legacy
    return { ...legacy, ...current }
  }

  export async function set(key: string, info: Info) {
    const file = Bun.file(filepath)
    const data = await all()
    await Bun.write(file, JSON.stringify({ ...data, [key]: info }, null, 2))
    await fs.chmod(file.name!, 0o600)
  }

  export async function remove(key: string) {
    const file = Bun.file(filepath)
    const data = await all()
    delete data[key]
    await Bun.write(file, JSON.stringify(data, null, 2))
    await fs.chmod(file.name!, 0o600)
  }
}
