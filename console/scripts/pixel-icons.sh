#!/bin/bash
# Builds assets/icons/pixel/*.svg from assets/icons/pixel.txt.
# Each "#" run in a row becomes one rect; crispEdges keeps pixels sharp.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
src=$root/assets/icons/pixel.txt
out=$root/assets/icons/pixel
mkdir -p "$out"
awk -v out="$out" '
  function flush() {
    if (name == "") return
    if (row != 12) { printf "pixel icon %s has %d rows, want 12\n", name, row > "/dev/stderr"; exit 1 }
    printf "<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 12 12\" shape-rendering=\"crispEdges\" fill=\"#000\">%s</svg>\n", rects > (out "/" name ".svg")
    close(out "/" name ".svg")
    name = ""; rects = ""; row = 0
  }
  /^#/ || /^[[:space:]]*$/ { next }
  /^[a-z-]+$/ { flush(); name = $0; next }
  {
    if (length($0) != 12) { printf "pixel icon %s row %d is %d wide, want 12\n", name, row, length($0) > "/dev/stderr"; exit 1 }
    x = 1
    while (x <= 12) {
      if (substr($0, x, 1) == "#") {
        s = x
        while (x <= 12 && substr($0, x, 1) == "#") x++
        rects = rects sprintf("<rect x=\"%d\" y=\"%d\" width=\"%d\" height=\"1\"/>", s - 1, row, x - s)
      } else x++
    }
    row++
  }
  END { flush() }
' "$src"
ls "$out" | wc -l
