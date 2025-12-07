import path from "path"
import { Global } from "../global"
import fs from "fs/promises"

export namespace Pin {
  const filepath = path.join(Global.Path.data, "pin.json")

  interface PinData {
    hash: string
    createdAt: number
  }

  export async function get(): Promise<PinData | undefined> {
    const file = Bun.file(filepath)
    return file.json().catch(() => undefined)
  }

  export async function exists(): Promise<boolean> {
    const data = await get()
    return !!data?.hash
  }

  export async function set(pin: string): Promise<void> {
    const hash = await Bun.password.hash(pin, {
      algorithm: "argon2id",
      memoryCost: 19456,
      timeCost: 2,
    })
    const file = Bun.file(filepath)
    await Bun.write(file, JSON.stringify({ hash, createdAt: Date.now() }, null, 2))
    await fs.chmod(filepath, 0o600)
  }

  export async function verify(pin: string): Promise<boolean> {
    const data = await get()
    if (!data?.hash) return false
    return Bun.password.verify(pin, data.hash)
  }

  export async function remove(): Promise<void> {
    await fs.unlink(filepath).catch(() => {})
  }
}
