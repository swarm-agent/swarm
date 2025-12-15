import { Server } from "../../server/server"
import { cmd } from "./cmd"
import { Profile } from "../../profile"
import { bootstrap } from "../bootstrap"

export const ServeCommand = cmd({
  command: "serve",
  builder: (yargs) =>
    yargs
      .option("port", {
        alias: ["p"],
        type: "number",
        describe: "port to listen on",
        default: 0,
      })
      .option("hostname", {
        type: "string",
        describe: "hostname to listen on",
        default: "127.0.0.1",
      })
      .option("profile", {
        type: "string",
        describe: "container profile to use (auto-starts if not running)",
      }),
  describe: "starts a headless opencode server",
  handler: async (args) => {
    const hostname = args.hostname
    const port = args.port

    await bootstrap(process.cwd(), async () => {
      // Initialize profiles from config FIRST (before checking for profile)
      await Profile.initFromConfig().catch((e) => {
        console.error("Failed to init profiles from config:", e)
      })

      // If profile specified, ensure container is running
      if (args.profile) {
        const profile = await Profile.get(args.profile)
        if (!profile) {
          console.error(`Profile '${args.profile}' not found. Create it with: swarm profile create ${args.profile} --image <image>`)
          process.exit(1)
        }
        if (profile.status !== "running") {
          console.log(`Starting container for profile '${args.profile}'...`)
          await Profile.start(args.profile)
          console.log(`Container started`)
        }
        // Store active profile for session creation
        process.env.OPENCODE_PROFILE = args.profile
      }

      const server = Server.listen({
        port,
        hostname,
      })
      console.log(`opencode server listening on http://${server.hostname}:${server.port}`)
      if (args.profile) {
        console.log(`Using container profile: ${args.profile}`)
      }
      await new Promise(() => {})
      await server.stop()
    })
  },
})
