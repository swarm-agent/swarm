/**
 * Tool-specific spinner/animation mappings
 * Each tool gets a unique spinner that represents its function
 */

export interface ToolSpinnerConfig {
  spinner: string // Spinner name from spinner-definitions.ts
  fallback?: string // Fallback spinner if primary not available
}

/**
 * Maps tool names to their specific spinner configurations
 * Each tool gets a unique spinner that visually represents its function
 */
export const TOOL_SPINNER_MAP: Record<string, ToolSpinnerConfig> = {
  // File reading - rotating scanner
  read: {
    spinner: "read_spinner", // Custom rotating brackets [|] [/] [-] [\]
    fallback: "snake_orbit",
  },

  // File writing - progressive typing animation
  write: {
    spinner: "write_spinner", // Progressive "writing..." animation
    fallback: "loading_bar",
  },

  // File editing - transformation spinner
  edit: {
    spinner: "edit_spinner", // Rotating edit indicator [ ] [/] [-] [\] [|]
    fallback: "sword_fight",
  },

  // Directory listing - growing bars
  list: {
    spinner: "list_spinner", // Growing vertical bars | || ||| ||||
    fallback: "equalizer",
  },

  // Pattern matching - spreading star
  glob: {
    spinner: "glob_spinner", // Spreading asterisk *  *   *    *
    fallback: "earth_spin",
  },

  // Text search - scanning progress
  grep: {
    spinner: "grep_spinner", // Progress bar with moving hash [#.........] [.......#.]
    fallback: "pong_match",
  },

  // Command execution - terminal cursor
  bash: {
    spinner: "bash_spinner", // Blinking cursor $ _
    fallback: "binary_rain",
  },

  // Web fetching - download progress bar
  webfetch: {
    spinner: "webfetch_spinner", // Download progress bar with color cycling
    fallback: "loading_bar",
  },

  // Todo reading - alternating TODO
  todoread: {
    spinner: "todoread_spinner", // TODO .... TODO ....
    fallback: "dice_roll",
  },

  // Todo writing - adding task
  todowrite: {
    spinner: "todowrite_spinner", // + task  +task   +task
    fallback: "fist_bump",
  },

  // Task agent - progress blocks
  task: {
    spinner: "task_spinner", // Moving blocks [####] [ ###] [  ##] [   #]
    fallback: "dna_helix",
  },

  // Exit plan mode
  "exit-plan-mode": {
    spinner: "weather_storm", // Transition/change
    fallback: "rotating_arrow",
  },

  // Web search - radar pulse
  websearch: {
    spinner: "websearch_spinner", // Radar/search pulse ◎·· ·◉· ··● ·◉·
    fallback: "bloom",
  },

  // Web contents - page loading
  webcontents: {
    spinner: "webcontents_spinner", // Page loading animation
    fallback: "loading_bar",
  },

  // Memory - floppy disk save animation
  memory: {
    spinner: "memory_spinner", // Floppy disk with write indicator
    fallback: "wave_block",
  },
}

/**
 * Get spinner name for a tool
 */
export function getToolSpinner(toolName: string): string {
  const config = TOOL_SPINNER_MAP[toolName]
  if (!config) {
    // Default spinner for unknown tools
    return "braille_fade"
  }
  return config.spinner
}

/**
 * Get fallback spinner for a tool
 */
export function getToolSpinnerFallback(toolName: string): string {
  const config = TOOL_SPINNER_MAP[toolName]
  if (!config || !config.fallback) {
    return "braille_fade"
  }
  return config.fallback
}

/**
 * Get all tools that have custom spinners
 */
export function getToolsWithCustomSpinners(): string[] {
  return Object.keys(TOOL_SPINNER_MAP)
}
