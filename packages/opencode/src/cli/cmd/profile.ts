import { cmd } from "./cmd"
import { Profile } from "../../profile"
import { Config } from "../../config/config"
import { ContainerRuntime } from "../../container/runtime"
import { bootstrap } from "../bootstrap"

export const ProfileCommand = cmd({
  command: "profile <command>",
  describe: "Manage container profiles for isolated execution",
  builder: (yargs) =>
    yargs
      .command(
        "list",
        "List all profiles",
        {},
        async () => {
          const profiles = await Profile.list()
          if (profiles.length === 0) {
            console.log("No profiles found. Create one with: swarm profile create <name> --image <image>")
            return
          }

          console.log("\nContainer Profiles:\n")
          for (const p of profiles) {
            const statusIcon =
              p.status === "running" ? "\x1b[32m●\x1b[0m" :
              p.status === "starting" ? "\x1b[33m◐\x1b[0m" :
              p.status === "error" ? "\x1b[31m✗\x1b[0m" :
              "\x1b[90m○\x1b[0m"

            console.log(`  ${statusIcon} ${p.name}`)
            console.log(`    Image: ${p.config.image}`)
            console.log(`    Status: ${p.status}${p.containerID ? ` (${p.containerID.slice(0, 12)})` : ""}`)
            if (p.config.keepAlive) {
              console.log(`    Keep Alive: ${p.config.idleTimeoutMinutes}min timeout`)
            }
            if (p.error) {
              console.log(`    Error: ${p.error}`)
            }
            console.log()
          }
        }
      )
      .command(
        "create <name>",
        "Create a new profile",
        (yargs) =>
          yargs
            .positional("name", {
              type: "string",
              describe: "Profile name",
              demandOption: true,
            })
            .option("image", {
              type: "string",
              describe: "Container image (e.g., ubuntu:22.04)",
              demandOption: true,
            })
            .option("workdir", {
              type: "string",
              describe: "Working directory in container",
              default: "/workspace",
            })
            .option("keep-alive", {
              type: "boolean",
              describe: "Keep container running between commands",
              default: true,
            })
            .option("idle-timeout", {
              type: "number",
              describe: "Stop container after idle (minutes, 0 = never)",
              default: 30,
            })
            .option("port", {
              type: "number",
              describe: "Port for swarm serve inside container",
            })
            .option("network", {
              type: "string",
              choices: ["bridge", "host", "none"] as const,
              describe: "Network mode",
              default: "bridge" as const,
            })
            .option("volume", {
              type: "array",
              string: true,
              describe: "Volume mount (host:container[:ro])",
            })
            .option("env", {
              type: "array",
              string: true,
              describe: "Environment variable (KEY=VALUE)",
            }),
        async (args) => {
          const volumes: Config.ContainerVolumeConfig[] = []
          if (args.volume) {
            for (const v of args.volume) {
              const parts = v.split(":")
              if (parts.length < 2) {
                console.error(`Invalid volume format: ${v}. Use host:container[:ro]`)
                process.exit(1)
              }
              volumes.push({
                host: parts[0],
                container: parts[1],
                readonly: parts[2] === "ro",
              })
            }
          }

          // Default: mount current directory
          if (volumes.length === 0) {
            volumes.push({ host: ".", container: "/workspace", readonly: false })
          }

          const environment: Record<string, string> = {}
          if (args.env) {
            for (const e of args.env) {
              const [key, ...valueParts] = e.split("=")
              environment[key] = valueParts.join("=")
            }
          }

          const profile = await Profile.create({
            name: args.name,
            image: args.image,
            workdir: args.workdir,
            keepAlive: args.keepAlive,
            idleTimeoutMinutes: args.idleTimeout,
            serverPort: args.port,
            network: { mode: args.network as "bridge" | "host" | "none" },
            volumes,
            environment,
          })

          console.log(`✓ Profile '${profile.name}' created`)
          console.log(`  Image: ${profile.config.image}`)
          console.log(`  Workdir: ${profile.config.workdir}`)
          console.log(`\nStart with: swarm profile start ${profile.name}`)
        }
      )
      .command(
        "show <name>",
        "Show profile details",
        (yargs) =>
          yargs.positional("name", {
            type: "string",
            describe: "Profile name",
            demandOption: true,
          }),
        async (args) => {
          const profile = await Profile.get(args.name)
          if (!profile) {
            console.error(`Profile '${args.name}' not found`)
            process.exit(1)
          }

          console.log(JSON.stringify(profile, null, 2))
        }
      )
      .command(
        "delete <name>",
        "Delete a profile",
        (yargs) =>
          yargs.positional("name", {
            type: "string",
            describe: "Profile name",
            demandOption: true,
          }),
        async (args) => {
          await Profile.remove(args.name)
          console.log(`✓ Profile '${args.name}' deleted`)
        }
      )
      .command(
        "start <name>",
        "Start a profile's container",
        (yargs) =>
          yargs
            .positional("name", {
              type: "string",
              describe: "Profile name",
              demandOption: true,
            })
            .option("pull", {
              type: "boolean",
              describe: "Pull image before starting",
              default: false,
            }),
        async (args) => {
          await bootstrap(process.cwd(), async () => {
            const profile = await Profile.get(args.name)
            if (!profile) {
              console.error(`Profile '${args.name}' not found`)
              process.exit(1)
            }

            // Check runtime availability first
            const config = await Config.get()
            const runtime = config.container?.runtime ?? "podman"
            const available = await ContainerRuntime.checkRuntime(runtime)
            if (!available) {
              console.error(`\n❌ ${runtime} is not installed or not running\n`)

              if (runtime === "podman") {
                console.error(`Podman is RECOMMENDED (rootless, no daemon needed):\n`)
                console.error(`  Arch:   sudo pacman -S podman`)
                console.error(`  Ubuntu: sudo apt install podman`)
                console.error(`  Fedora: sudo dnf install podman`)
                console.error(`  macOS:  brew install podman`)
                console.error(`\nAfter install, just run: swarm profile start ${args.name}`)
              } else {
                console.error(`Docker requires a daemon running as root:\n`)
                console.error(`  1. Install: https://docs.docker.com/get-docker/`)
                console.error(`  2. Start daemon: sudo systemctl start docker`)
                console.error(`  3. (Optional) Enable auto-start: sudo systemctl enable docker`)
                console.error(`\nAlternatively, use podman (no daemon needed):`)
                console.error(`  Remove "runtime": "docker" from opencode.json`)
                console.error(`  Install podman: sudo pacman -S podman`)
              }
              process.exit(1)
            }

            if (args.pull) {
              console.log(`Pulling ${profile.config.image}...`)
              await ContainerRuntime.pull(runtime, profile.config.image)
            }

            console.log(`Starting container for '${args.name}'...`)
            const containerID = await Profile.start(args.name)
            console.log(`✓ Container started: ${containerID.slice(0, 12)}`)
          })
        }
      )
      .command(
        "stop <name>",
        "Stop a profile's container",
        (yargs) =>
          yargs.positional("name", {
            type: "string",
            describe: "Profile name",
            demandOption: true,
          }),
        async (args) => {
          await bootstrap(process.cwd(), async () => {
            console.log(`Stopping container for '${args.name}'...`)
            await Profile.stop(args.name)
            console.log(`✓ Container stopped`)
          })
        }
      )
      .command(
        "restart <name>",
        "Restart a profile's container",
        (yargs) =>
          yargs.positional("name", {
            type: "string",
            describe: "Profile name",
            demandOption: true,
          }),
        async (args) => {
          await bootstrap(process.cwd(), async () => {
            console.log(`Restarting container for '${args.name}'...`)
            const containerID = await Profile.restart(args.name)
            console.log(`✓ Container restarted: ${containerID.slice(0, 12)}`)
          })
        }
      )
      .command(
        "status [name]",
        "Show container status",
        (yargs) =>
          yargs.positional("name", {
            type: "string",
            describe: "Profile name (optional, shows all if not specified)",
          }),
        async (args) => {
          if (args.name) {
            const status = await Profile.status(args.name)
            console.log(`Profile: ${args.name}`)
            console.log(`  Status: ${status.status}`)
            console.log(`  Running: ${status.running}`)
            if (status.containerID) {
              console.log(`  Container: ${status.containerID.slice(0, 12)}`)
            }
          } else {
            const profiles = await Profile.list()
            if (profiles.length === 0) {
              console.log("No profiles found")
              return
            }
            for (const p of profiles) {
              const status = await Profile.status(p.name)
              const icon = status.running ? "\x1b[32m●\x1b[0m" : "\x1b[90m○\x1b[0m"
              console.log(`${icon} ${p.name}: ${status.status}`)
            }
          }
        }
      )
      .command(
        "logs <name>",
        "Show container logs",
        (yargs) =>
          yargs
            .positional("name", {
              type: "string",
              describe: "Profile name",
              demandOption: true,
            })
            .option("follow", {
              alias: "f",
              type: "boolean",
              describe: "Follow log output",
              default: false,
            })
            .option("tail", {
              type: "number",
              describe: "Number of lines to show",
              default: 100,
            }),
        async (args) => {
          const profile = await Profile.get(args.name)
          if (!profile || !profile.containerID) {
            console.error(`No running container for '${args.name}'`)
            process.exit(1)
          }

          for await (const line of ContainerRuntime.logs(profile.containerID, {
            follow: args.follow,
            tail: args.tail,
          })) {
            process.stdout.write(line)
          }
        }
      )
      .command(
        "exec <name> [cmd..]",
        "Execute a command in a profile's container",
        (yargs) =>
          yargs
            .positional("name", {
              type: "string",
              describe: "Profile name",
              demandOption: true,
            })
            .positional("cmd", {
              type: "string",
              array: true,
              describe: "Command to execute",
            }),
        async (args) => {
          await bootstrap(process.cwd(), async () => {
            const command = args.cmd ?? ["/bin/sh"]
            const result = await Profile.exec(args.name, command)
            process.stdout.write(result.stdout)
            process.stderr.write(result.stderr)
            process.exit(result.exitCode)
          })
        }
      )
      .demandCommand(1, "You need to specify a command"),
  handler: () => {},
})
