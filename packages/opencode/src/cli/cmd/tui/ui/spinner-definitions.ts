export interface SpinnerDef {
  name: string
  frames: string[]
  interval: number
  mode: "normal" | "cyber" | "neon" | "rgb" | "glitch"
}

export const SPINNERS: Record<string, SpinnerDef> = {
  // Existing spinners from the codebase
  braille_fade: {
    name: "braille_fade",
    frames: ["⣿", "⣷", "⣯", "⣟", "⡿", "⢿", "⣻", "⣽", "⣾"],
    interval: 80,
    mode: "cyber",
  },
  snake_orbit: {
    name: "snake_orbit",
    frames: ["⠋", "⠙", "⠚", "⠞", "⠖", "⠦", "⠴", "⠲", "⠳", "⠓"],
    interval: 80,
    mode: "neon",
  },
  square_rgb: {
    name: "square_rgb",
    frames: ["▖", "▘", "▝", "▗"],
    interval: 100,
    mode: "rgb",
  },
  dna_helix: {
    name: "dna_helix",
    frames: ["⠁⠂", "⠄⠂", "⡀⠄", "⡀⢀", "⠠⢀", "⠐⠠", "⠈⠐", "⠈⠁", "⠐⠁", "⠠⠁", "⢀⠠", "⢀⡀", "⠄⡀", "⠂⠄", "⠂⠁"],
    interval: 80,
    mode: "cyber",
  },

  // New Gemini spinners
  dhalsim: {
    name: "dhalsim",
    frames: ["o-|-<", "o--|-<", "o---|-<", "o----|-<", "o-----|-<", "o----|-<", "o---|-<", "o--|-<"],
    interval: 100,
    mode: "normal",
  },
  moon_phase: {
    name: "moon_phase",
    frames: [
      "(     )",
      "(|    )",
      "(||   )",
      "(|||  )",
      "(|||| )",
      "(|||||)",
      "( ||||)",
      "(  |||)",
      "(   ||)",
      "(    |)",
    ],
    interval: 100,
    mode: "normal",
  },
  clock_spin: {
    name: "clock_spin",
    frames: ["|", "/", "-", "\\"],
    interval: 80,
    mode: "normal",
  },
  grenade: {
    name: "grenade",
    frames: ["( )", "(!)", "(o)", "(O)", "(X)", ">X<", " * ", "   "],
    interval: 200,
    mode: "glitch",
  },
  pong_match: {
    name: "pong_match",
    frames: [
      "| .      |",
      "|  .     |",
      "|   .    |",
      "|    .   |",
      "|     .  |",
      "|      . |",
      "|       .|",
      "|      . |",
      "|     .  |",
      "|    .   |",
      "|   .    |",
      "|  .     |",
      "| .      |",
    ],
    interval: 60,
    mode: "neon",
  },
  wave_block: {
    name: "wave_block",
    frames: ["█", "▓", "▒", "░", " ", "░", "▒", "▓"],
    interval: 100,
    mode: "rgb",
  },
  shuriken: {
    name: "shuriken",
    frames: [" + ", " x ", " + ", " x "],
    interval: 150,
    mode: "cyber",
  },
  run_cat: {
    name: "run_cat",
    frames: ["^._.^", "-._.-", "^._.^", "-._.-"],
    interval: 300,
    mode: "normal",
  },
  pacman: {
    name: "pacman",
    frames: ["C . . .", " C . . ", "  C . .", "   C . ", "    C .", "     C ", "      C", "     C ", "    C :"],
    interval: 120,
    mode: "rgb",
  },
  dice_roll: {
    name: "dice_roll",
    frames: ["[1]", "[2]", "[3]", "[4]", "[5]", "[6]"],
    interval: 150,
    mode: "normal",
  },
  loading_bar: {
    name: "loading_bar",
    frames: [
      "[          ]",
      "[=         ]",
      "[==        ]",
      "[===       ]",
      "[====      ]",
      "[=====     ]",
      "[======    ]",
      "[=======   ]",
      "[========  ]",
      "[========= ]",
      "[==========]",
    ],
    interval: 80,
    mode: "cyber",
  },
  earth_spin: {
    name: "earth_spin",
    frames: ["(.. )", "( ..)", "(  .)", "( . )"],
    interval: 180,
    mode: "normal",
  },
  shark_hunt: {
    name: "shark_hunt",
    frames: ["><>      |>", " ><>    |> ", "  ><>  |>  ", "   ><>|>   ", "    >|<    ", "     X     "],
    interval: 150,
    mode: "glitch",
  },
  equalizer: {
    name: "equalizer",
    frames: [" .", ":.", "::", "||", ":|", ".:", " ."],
    interval: 100,
    mode: "rgb",
  },
  sword_fight: {
    name: "sword_fight",
    frames: ["--o /o--", "--o/o--", "--X--", "  / \\  "],
    interval: 300,
    mode: "normal",
  },
  triangle_morph: {
    name: "triangle_morph",
    frames: ["◢", "◣", "◤", "◥"],
    interval: 100,
    mode: "neon",
  },
  binary_rain: {
    name: "binary_rain",
    frames: ["10101", "01010", "11011", "00100", "11111", "00000"],
    interval: 80,
    mode: "cyber",
  },
  heart_beat: {
    name: "heart_beat",
    frames: ["<3", "< 3", "<  3", "< 3"],
    interval: 300,
    mode: "rgb",
  },
  weather_storm: {
    name: "weather_storm",
    frames: [" / / ", "/ / /", " / / ", "/ / /"],
    interval: 200,
    mode: "normal",
  },
  fist_bump: {
    name: "fist_bump",
    frames: ["o/      \\o", " o/    \\o ", "  o/  \\o  ", "   o/\\o   ", "    XX    ", "   *XX*   "],
    interval: 150,
    mode: "rgb",
  },

  // Classic expanding ring (existing in codebase)
  expanding_ring: {
    name: "expanding_ring",
    frames: ["●", "◉", "◎", "○"],
    interval: 200,
    mode: "cyber",
  },

  // Rotating quarters (existing in codebase)
  rotating_quarters: {
    name: "rotating_quarters",
    frames: ["●", "◐", "◓", "◑", "◒"],
    interval: 150,
    mode: "neon",
  },

  // Rotating arrow (existing in codebase)
  rotating_arrow: {
    name: "rotating_arrow",
    frames: ["↑", "↗", "→", "↘", "↓", "↙", "←", "↖"],
    interval: 120,
    mode: "normal",
  },

  // Quarter circles (existing in codebase)
  quarter_circles: {
    name: "quarter_circles",
    frames: ["◜", "◝", "◞", "◟"],
    interval: 150,
    mode: "neon",
  },

  // Bloom effect (existing in codebase)
  bloom: {
    name: "bloom",
    frames: ["·", "∘", "○", "◎", "◉", "●", "◉", "◎", "○", "∘"],
    interval: 120,
    mode: "rgb",
  },

  // Custom Claude Edit spinner - code transformation animation
  claude_edit: {
    name: "claude_edit",
    frames: [
      "[ C ]____",
      "[  L ]___",
      "[   A ]__",
      "[    U ]_",
      "[     D ]",
      "[    E ]_",
      "[   > ]__",
      "[  ✓ ]___",
      "[ ✓✓ ]___",
      "[✓✓✓ ]___",
    ],
    interval: 100,
    mode: "cyber",
  },

  // Tool-specific spinners with unique animations
  read_spinner: {
    name: "read_spinner",
    frames: ["[|]", "[/]", "[-]", "[\\]"],
    interval: 200,
    mode: "normal",
  },

  write_spinner: {
    name: "write_spinner",
    frames: [
      "[>█-------]",
      "[->█------]",
      "[-->█-----]",
      "[--->█----]",
      "[---->█---]",
      "[----->█--]",
      "[------>█-]",
      "[------->█]",
      "[---------]",
      "[ <-------]",
      "[  <------]",
      "[   <-----]",
      "[    <----]",
      "[     <---]",
      "[      <--]",
      "[       <-]",
      "[        <]",
    ],
    interval: 150,
    mode: "normal",
  },

  edit_spinner: {
    name: "edit_spinner",
    frames: ["_", "[", "[>", "[=]", "{=}", "{<}", "<", "_"],
    interval: 100,
    mode: "cyber",
  },

  list_spinner: {
    name: "list_spinner",
    frames: ["|", "||", "|||", "||||", "|||", "||", "|", " "],
    interval: 150,
    mode: "normal",
  },

  glob_spinner: {
    name: "glob_spinner",
    frames: ["*", " *", "  *", "   *", "    *", "   *", "  *", " *"],
    interval: 100,
    mode: "neon",
  },

  grep_spinner: {
    name: "grep_spinner",
    frames: [
      "[#.........]",
      "[.#........]",
      "[..#.......]",
      "[...#......]",
      "[....#.....]",
      "[.....#....]",
      "[......#...]",
      "[.......#..]",
      "[........#.]",
      "[.........#]",
    ],
    interval: 60,
    mode: "cyber",
  },

  bash_spinner: {
    name: "bash_spinner",
    frames: ["$ _", "$  "],
    interval: 500,
    mode: "normal",
  },

  // Webfetch - download arrows wave (fallback - main animation is WebfetchSpinner component)
  webfetch_spinner: {
    name: "webfetch_spinner",
    frames: ["↓↓↓", "↓↓↓", "↓↓↓", "↓↓↓"],
    interval: 150,
    mode: "rgb",
  },

  todoread_spinner: {
    name: "todoread_spinner",
    frames: ["TODO", "....", "TODO", "...."],
    interval: 500,
    mode: "normal",
  },

  todowrite_spinner: {
    name: "todowrite_spinner",
    frames: ["+ task", " +task", "  +task"],
    interval: 150,
    mode: "neon",
  },

  task_spinner: {
    name: "task_spinner",
    frames: ["[####]", "[ ###]", "[  ##]", "[   #]", "[    ]", "[#   ]", "[##  ]", "[### ]"],
    interval: 80,
    mode: "rgb",
  },

  // ============================================================================
  // TYPE B: TRAVELING EDIT/WRITE ANIMATIONS - Nerdfont icons that move
  // ============================================================================

  // Edit: Rhombus Train - Diamond travels left to right
  edit_rhombus_train: {
    name: "edit_rhombus_train",
    frames: ["󰜋·····", "·󰜋····", "··󰜋···", "···󰜋··", "····󰜋·", "·····󰜋", "····󰜋·", "···󰜋··", "··󰜋···", "·󰜋····"],
    interval: 100,
    mode: "cyber",
  },

  // Write: Hexagon Slide - Hexagon slides with fading trail
  write_hexagon_slide: {
    name: "write_hexagon_slide",
    frames: [
      "󰋘·····",
      "󰋙󰋘····",
      "·󰋙󰋘···",
      "··󰋙󰋘··",
      "···󰋙󰋘·",
      "····󰋙󰋘",
      "·····󰋘",
      "····󰋘·",
      "···󰋘··",
      "··󰋘···",
      "·󰋘····",
    ],
    interval: 80,
    mode: "neon",
  },

  // Write: Circle Bounce - Circle bounces between ends
  write_circle_bounce: {
    name: "write_circle_bounce",
    frames: ["●·····", "·●····", "··●···", "···●··", "····●·", "·····●", "····●·", "···●··", "··●···", "·●····"],
    interval: 60,
    mode: "rgb",
  },

  // Edit: Ray Expansion - Rays expand outward from center
  edit_ray_expand: {
    name: "edit_ray_expand",
    frames: ["··󰑅··", "·󰑂󰑅󰑀·", "󰑂·󰑅·󰑀", "·󰑂󰑅󰑀·", "··󰑅··"],
    interval: 150,
    mode: "cyber",
  },

  // Edit: Delta Morph - Triangle morphs through shapes
  edit_delta_morph: {
    name: "edit_delta_morph",
    frames: ["󰇂", "󰜌", "󰜋", "󱓼", "󱓻", "󱓼", "󰜋", "󰜌"],
    interval: 120,
    mode: "neon",
  },

  // Write: Star Pulse Train - Stars travel with pulse
  write_star_train: {
    name: "write_star_train",
    frames: ["󰫢·····", "·󰫢····", "··󰫢···", "···󰫢··", "····󰫢·", "·····󰫢", "····󰓎·", "···󰓎··", "··󰓎···", "·󰓎····"],
    interval: 90,
    mode: "rgb",
  },

  // ============================================================================
  // COMPACTING ANIMATIONS - Memory compression visualizations
  // ============================================================================

  // Compacting wave - flowing compression effect
  compact_wave: {
    name: "compact_wave",
    frames: [
      "▁▂▃▄▅▆▇█▇▆▅▄▃▂▁",
      "▂▃▄▅▆▇█▇▆▅▄▃▂▁▁",
      "▃▄▅▆▇█▇▆▅▄▃▂▁▁▂",
      "▄▅▆▇█▇▆▅▄▃▂▁▁▂▃",
      "▅▆▇█▇▆▅▄▃▂▁▁▂▃▄",
      "▆▇█▇▆▅▄▃▂▁▁▂▃▄▅",
      "▇█▇▆▅▄▃▂▁▁▂▃▄▅▆",
      "█▇▆▅▄▃▂▁▁▂▃▄▅▆▇",
    ],
    interval: 80,
    mode: "neon",
  },

  // Compacting brain pulse - neural compression
  compact_brain: {
    name: "compact_brain",
    frames: ["  ◯  ", " ◯◯◯ ", "◯◯◯◯◯", " ◯◯◯ ", "  ◯  ", "  ●  ", " ●●● ", "●●●●●", " ●●● ", "  ●  "],
    interval: 100,
    mode: "rgb",
  },

  // Compacting shrink - gradient shrinking bar
  compact_shrink: {
    name: "compact_shrink",
    frames: [
      "████████████████",
      "▓███████████████",
      "▒▓██████████████",
      "░▒▓█████████████",
      " ░▒▓████████████",
      "  ░▒▓███████████",
      "   ░▒▓██████████",
      "    ░▒▓█████████",
      "     ░▒▓████████",
      "      ░▒▓███████",
      "       ░▒▓██████",
      "        ░▒▓█████",
    ],
    interval: 60,
    mode: "cyber",
  },
}

// Helper function to get spinner by name with fallback
export function getSpinner(name: string): SpinnerDef {
  return SPINNERS[name] ?? SPINNERS.braille_fade
}

// Get all spinner names
export function getSpinnerNames(): string[] {
  return Object.keys(SPINNERS)
}

// Get spinners by mode
export function getSpinnersByMode(mode: SpinnerDef["mode"]): SpinnerDef[] {
  return Object.values(SPINNERS).filter((s) => s.mode === mode)
}
