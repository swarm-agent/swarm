import { Installation } from "@/installation"
import { Server } from "@/server/server"
import { Log } from "@/util/log"
import { Instance } from "@/project/instance"
import { Rpc } from "@/util/rpc"
import { upgrade } from "@/cli/upgrade"
import { Hyprland } from "@/hyprland"

await Log.init({
  print: process.argv.includes("--print-logs"),
  dev: Installation.isLocal(),
  level: (() => {
    if (Installation.isLocal()) return "DEBUG"
    return "INFO"
  })(),
})

process.on("unhandledRejection", (e) => {
  Log.Default.error("rejection", {
    e: e instanceof Error ? e.message : e,
  })
})

process.on("uncaughtException", (e) => {
  Log.Default.error("exception", {
    e: e instanceof Error ? e.message : e,
  })
})

// Ensure Hyprland session cleanup on exit
const cleanupHyprland = () => {
  Hyprland.unregister().catch(() => {})
}
process.on("exit", cleanupHyprland)
process.on("SIGINT", () => {
  cleanupHyprland()
  process.exit(0)
})
process.on("SIGTERM", () => {
  cleanupHyprland()
  process.exit(0)
})

upgrade()

let server: Bun.Server<undefined>
export const rpc = {
  async server(input: { port: number; hostname: string; hyprland?: boolean }) {
    if (server) await server.stop(true)
    try {
      server = Server.listen(input)
      // Initialize and register with Hyprland session tracker if enabled
      Hyprland.init(input.hyprland ?? false)
      await Hyprland.register(process.cwd())
      return {
        url: server.url.toString(),
      }
    } catch (e) {
      console.error(e)
      throw e
    }
  },
  async shutdown() {
    await Hyprland.unregister()
    await Instance.disposeAll()
    await server.stop(true)
  },
}

Rpc.listen(rpc)
