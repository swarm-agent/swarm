import { createStore } from "solid-js/store"
import path from "path"
import { Global } from "@/global"
import { createSimpleContext } from "./helper"

const WORKSPACE_FILE = path.join(Global.Path.state, "workspace.json")

export const { use: useWorkspace, provider: WorkspaceProvider } = createSimpleContext({
  name: "Workspace",
  init: () => {
    const [store, setStore] = createStore<{ dirs: string[]; ready: boolean }>({
      dirs: [],
      ready: false,
    })

    const file = Bun.file(WORKSPACE_FILE)

    // Load workspace dirs on init
    file
      .json()
      .then((x: { dirs?: string[] }) => {
        setStore("dirs", x.dirs ?? [])
      })
      .catch(() => {
        // File doesn't exist yet, that's fine
      })
      .finally(() => {
        setStore("ready", true)
      })

    function persist() {
      Bun.write(file, JSON.stringify({ dirs: store.dirs }, null, 2))
    }

    return {
      get ready() {
        return store.ready
      },

      /** Get all workspace directories */
      list() {
        return store.dirs
      },

      /** Add a directory to the workspace */
      add(dir: string) {
        const resolved = path.resolve(dir)
        if (store.dirs.includes(resolved)) return // Already exists
        const updated = [...store.dirs, resolved]
        setStore("dirs", updated)
        persist()
      },

      /** Remove a directory from the workspace */
      remove(dir: string) {
        const resolved = path.resolve(dir)
        const updated = store.dirs.filter((d) => d !== resolved)
        setStore("dirs", updated)
        persist()
      },

      /** Check if a filepath is within any workspace directory */
      contains(filepath: string) {
        const resolved = path.resolve(filepath)
        return store.dirs.some((d) => resolved === d || resolved.startsWith(d + path.sep))
      },

      /** Check if a directory is a workspace directory */
      has(dir: string) {
        const resolved = path.resolve(dir)
        return store.dirs.includes(resolved)
      },
    }
  },
})

/**
 * Read workspace dirs from file (for use outside TUI context)
 * This is used by tools to check if a path is in a workspace dir
 */
export async function readWorkspaceDirs(): Promise<string[]> {
  try {
    const file = Bun.file(WORKSPACE_FILE)
    const data = (await file.json()) as { dirs?: string[] }
    return data.dirs ?? []
  } catch {
    return []
  }
}

/**
 * Check if a filepath is within any workspace directory (for use outside TUI context)
 */
export async function isInWorkspaceDir(filepath: string): Promise<boolean> {
  const dirs = await readWorkspaceDirs()
  const resolved = path.resolve(filepath)
  return dirs.some((d) => resolved === d || resolved.startsWith(d + path.sep))
}
