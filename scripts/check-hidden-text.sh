#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

echo "[hidden-text-check] scanning tracked and untracked non-ignored text files for hidden/control characters"

python3 - "${ROOT_DIR}" <<'PY'
import pathlib
import subprocess
import sys

root = pathlib.Path(sys.argv[1])

dangerous = {
    0x202A: "LRE",
    0x202B: "RLE",
    0x202C: "PDF",
    0x202D: "LRO",
    0x202E: "RLO",
    0x2066: "LRI",
    0x2067: "RLI",
    0x2068: "FSI",
    0x2069: "PDI",
    0x200E: "LRM",
    0x200F: "RLM",
    0x061C: "ALM",
    0x200B: "ZWSP",
    0x200C: "ZWNJ",
    0x200D: "ZWJ",
    0xFEFF: "BOM",
    0x007F: "DEL",
}

allowed = {
    ("internal/ui/home_auth_modal.go", 597, 49, 0x001B),
    ("internal/ui/home_auth_modal.go", 597, 63, 0x001B),
}


def git_paths(*args: str) -> list[str]:
    out = subprocess.check_output(["git", "ls-files", *args], cwd=root)
    return [item.decode("utf-8", errors="strict") for item in out.split(b"\x00") if item]


paths: list[str] = []
seen: set[str] = set()
for rel in git_paths("-z") + git_paths("--others", "--exclude-standard", "-z"):
    if rel in seen:
        continue
    seen.add(rel)
    paths.append(rel)

findings: list[str] = []
non_utf8: list[str] = []

for rel in paths:
    path = root / rel
    if not path.is_file():
        continue
    data = path.read_bytes()
    if b"\x00" in data:
        continue
    try:
        text = data.decode("utf-8")
    except UnicodeDecodeError:
        non_utf8.append(rel)
        continue

    for line_no, line in enumerate(text.splitlines(), 1):
        for col_no, ch in enumerate(line, 1):
            cp = ord(ch)
            if (rel, line_no, col_no, cp) in allowed:
                continue
            if cp in dangerous:
                findings.append(f"{rel}:{line_no}:{col_no}: U+{cp:04X} {dangerous[cp]}")
            elif cp < 32 and cp not in (9,):
                findings.append(f"{rel}:{line_no}:{col_no}: U+{cp:04X} CTRL")

if non_utf8:
    print("[hidden-text-check] FAIL: non-UTF-8 text files found:")
    for rel in non_utf8:
        print(rel)
    sys.exit(1)

if findings:
    print("[hidden-text-check] FAIL: suspicious hidden/control characters found:")
    for item in findings:
        print(item)
    sys.exit(1)

print("[hidden-text-check] PASS")
PY
