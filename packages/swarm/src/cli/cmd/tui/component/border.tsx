export const SplitBorder = {
  border: ["left" as const, "right" as const],
  customBorderChars: {
    topLeft: "",
    bottomLeft: "",
    vertical: "┃",
    topRight: "",
    bottomRight: "",
    horizontal: "",
    bottomT: "",
    topT: "",
    cross: "",
    leftT: "",
    rightT: "",
  },
}

export const RoundedBorder = {
  customBorderChars: {
    topLeft: "╭",
    topRight: "╮",
    bottomLeft: "╰",
    bottomRight: "╯",
    vertical: "│",
    horizontal: "─",
    bottomT: "┴",
    topT: "┬",
    cross: "┼",
    leftT: "├",
    rightT: "┤",
  },
}
