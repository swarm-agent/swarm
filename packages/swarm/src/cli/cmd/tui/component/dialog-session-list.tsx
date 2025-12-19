import { useDialog } from "@tui/ui/dialog"
import { DialogSelect } from "@tui/ui/dialog-select"
import { useRoute } from "@tui/context/route"
import { useSync } from "@tui/context/sync"
import { createEffect, createMemo, createSignal, onMount } from "solid-js"
import { Locale } from "@/util/locale"
import { Keybind } from "@/util/keybind"
import { useTheme } from "../context/theme"
import { useSDK } from "../context/sdk"
import { DialogSessionRename } from "./dialog-session-rename"

export function DialogSessionList() {
  const dialog = useDialog()
  const sync = useSync()
  const { theme } = useTheme()
  const route = useRoute()
  const sdk = useSDK()

  const [toDelete, setToDelete] = createSignal<string>()

  const deleteKeybind = "ctrl+d"

  const currentSessionID = createMemo(() => (route.data.type === "session" ? route.data.sessionID : undefined))

  const options = createMemo(() => {
    const today = new Date().toDateString()
    const parentSessions = sync.data.session.filter((x) => x.parentID === undefined)

    // Build a map of parent -> children for quick lookup
    const childrenMap = new Map<string, typeof sync.data.session>()
    for (const session of sync.data.session) {
      if (session.parentID) {
        const existing = childrenMap.get(session.parentID) ?? []
        existing.push(session)
        childrenMap.set(session.parentID, existing)
      }
    }

    return parentSessions
      .map((x) => {
        const date = new Date(x.time.updated)
        let category = date.toDateString()
        if (category === today) {
          category = "Today"
        }
        const isDeleting = toDelete() === x.id

        // Get children for this session
        const children = childrenMap.get(x.id) ?? []

        // Build children summary - show agent names from child titles
        let childrenSummary = ""
        if (children.length > 0) {
          const agentNames = children
            .map((child) => {
              // Extract agent name from title like "Description (@agentname subagent)"
              const match = child.title.match(/@(\w+)\s+subagent/)
              return match ? match[1] : null
            })
            .filter((name) => name !== null)

          if (agentNames.length > 0) {
            const uniqueAgents = [...new Set(agentNames)]
            childrenSummary = ` • ${children.length} subagent${children.length > 1 ? "s" : ""} (${uniqueAgents.join(", ")})`
          }
        }

        return {
          title: isDeleting ? `Press ${deleteKeybind} again to confirm` : x.title + childrenSummary,
          bg: isDeleting ? theme.error : undefined,
          value: x.id,
          category,
          footer: Locale.time(x.time.updated),
        }
      })
      .slice(0, 150)
  })

  createEffect(() => {
    console.log("session count", sync.data.session.length)
  })

  onMount(() => {
    dialog.setSize("large")
  })

  return (
    <DialogSelect
      title="Sessions"
      options={options()}
      current={currentSessionID()}
      onMove={() => {
        setToDelete(undefined)
      }}
      onSelect={(option) => {
        route.navigate({
          type: "session",
          sessionID: option.value,
        })
        dialog.clear()
      }}
      keybind={[
        {
          keybind: Keybind.parse(deleteKeybind)[0],
          title: "delete",
          onTrigger: async (option) => {
            if (toDelete() === option.value) {
              sdk.client.session.delete({
                path: {
                  id: option.value,
                },
              })
              setToDelete(undefined)
              // dialog.clear()
              return
            }
            setToDelete(option.value)
          },
        },
        {
          keybind: Keybind.parse("ctrl+r")[0],
          title: "rename",
          onTrigger: async (option) => {
            dialog.replace(() => <DialogSessionRename session={option.value} />)
          },
        },
      ]}
    />
  )
}
