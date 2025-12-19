export namespace Flag {
  export const SWARM_AUTO_SHARE = truthy("SWARM_AUTO_SHARE")
  export const SWARM_CONFIG = process.env["SWARM_CONFIG"]
  export const SWARM_CONFIG_DIR = process.env["SWARM_CONFIG_DIR"]
  export const SWARM_CONFIG_CONTENT = process.env["SWARM_CONFIG_CONTENT"]
  export const SWARM_DISABLE_AUTOUPDATE = truthy("SWARM_DISABLE_AUTOUPDATE")
  export const SWARM_DISABLE_PRUNE = truthy("SWARM_DISABLE_PRUNE")
  export const SWARM_PERMISSION = process.env["SWARM_PERMISSION"]
  // SDK sandbox override - applied LAST, REPLACES entire sandbox config
  export const SWARM_SANDBOX = process.env["SWARM_SANDBOX"]
  export const SWARM_DISABLE_DEFAULT_PLUGINS = truthy("SWARM_DISABLE_DEFAULT_PLUGINS")
  export const SWARM_DISABLE_LSP_DOWNLOAD = truthy("SWARM_DISABLE_LSP_DOWNLOAD")
  export const SWARM_ENABLE_EXPERIMENTAL_MODELS = truthy("SWARM_ENABLE_EXPERIMENTAL_MODELS")
  export const SWARM_DISABLE_AUTOCOMPACT = truthy("SWARM_DISABLE_AUTOCOMPACT")
  export const SWARM_FAKE_VCS = process.env["SWARM_FAKE_VCS"]

  // Experimental
  export const SWARM_EXPERIMENTAL = truthy("SWARM_EXPERIMENTAL")
  export const SWARM_EXPERIMENTAL_WATCHER = SWARM_EXPERIMENTAL || truthy("SWARM_EXPERIMENTAL_WATCHER")
  export const SWARM_EXPERIMENTAL_EXA = SWARM_EXPERIMENTAL || truthy("SWARM_EXPERIMENTAL_EXA")

  function truthy(key: string) {
    const value = process.env[key]?.toLowerCase()
    return value === "true" || value === "1"
  }
}
