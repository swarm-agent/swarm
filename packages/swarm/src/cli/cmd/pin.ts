import { cmd } from "./cmd"
import { Pin } from "../../auth/pin"
import { UI } from "../ui"
import * as prompts from "@clack/prompts"
import { Instance } from "../../project/instance"
import path from "path"
import os from "os"
import { Global } from "../../global"

export const PinCommand = cmd({
  command: "pin",
  describe: "manage PIN for protected commands",
  builder: (yargs) => yargs.command(PinSetCommand).command(PinRemoveCommand).command(PinStatusCommand).demandCommand(),
  async handler() {},
})

export const PinSetCommand = cmd({
  command: "set",
  describe: "set your PIN for protected commands",
  async handler() {
    await Instance.provide({
      directory: process.cwd(),
      async fn() {
        UI.empty()
        prompts.intro("Set PIN")

        const existingPin = await Pin.exists()
        if (existingPin) {
          const confirm = await prompts.confirm({
            message: "A PIN is already configured. Replace it?",
          })
          if (prompts.isCancel(confirm) || !confirm) {
            prompts.cancel("Cancelled")
            return
          }
        }

        const pin = await prompts.password({
          message: "Enter new PIN (min 4 characters):",
          validate: (v) => {
            if (!v || v.length < 4) return "PIN must be at least 4 characters"
          },
        })

        if (prompts.isCancel(pin)) {
          prompts.cancel("Cancelled")
          return
        }

        const confirm = await prompts.password({
          message: "Confirm PIN:",
        })

        if (prompts.isCancel(confirm)) {
          prompts.cancel("Cancelled")
          return
        }

        if (pin !== confirm) {
          prompts.log.error("PINs do not match")
          return
        }

        await Pin.set(pin)
        prompts.log.success("PIN set successfully")

        const pinPath = path.join(Global.Path.data, "pin.json")
        const homedir = os.homedir()
        const displayPath = pinPath.startsWith(homedir) ? pinPath.replace(homedir, "~") : pinPath
        prompts.log.info(`PIN hash stored at: ${UI.Style.TEXT_DIM}${displayPath}`)

        prompts.outro("Done")
      },
    })
  },
})

export const PinRemoveCommand = cmd({
  command: "remove",
  describe: "remove your PIN",
  async handler() {
    UI.empty()
    prompts.intro("Remove PIN")

    const hasPin = await Pin.exists()
    if (!hasPin) {
      prompts.log.warn("No PIN is configured")
      prompts.outro("Done")
      return
    }

    const confirm = await prompts.confirm({
      message: "Are you sure you want to remove your PIN?",
    })

    if (prompts.isCancel(confirm) || !confirm) {
      prompts.cancel("Cancelled")
      return
    }

    await Pin.remove()
    prompts.log.success("PIN removed")
    prompts.outro("Done")
  },
})

export const PinStatusCommand = cmd({
  command: "status",
  describe: "check if PIN is configured",
  async handler() {
    UI.empty()
    const hasPin = await Pin.exists()

    const pinPath = path.join(Global.Path.data, "pin.json")
    const homedir = os.homedir()
    const displayPath = pinPath.startsWith(homedir) ? pinPath.replace(homedir, "~") : pinPath

    prompts.intro(`PIN Status ${UI.Style.TEXT_DIM}${displayPath}`)

    if (hasPin) {
      prompts.log.success("PIN is configured")
    } else {
      prompts.log.warn("No PIN configured")
      prompts.log.info("Run: opencode pin set")
    }

    prompts.outro("")
  },
})
