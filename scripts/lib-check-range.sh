#!/usr/bin/env bash
# Shared by the lexical repository checks (check-hardcoded-paths.sh,
# check-daemon-storage-paths.sh).
#
# Requirement: a protected push is judged on the lines its commits add, not on
# reviewed content that is already on the target branch.
# Authority: scripts/check-prepush.sh exports SWARM_CHECK_BASE (the commit the
# remote already has) and SWARM_CHECK_HEAD (the pushed commit). Without
# SWARM_CHECK_BASE every check keeps scanning the whole working tree.
#
# swarm_check_range_tree DIR writes, under DIR, every file the range adds or
# modifies at its repository path. Only the added lines keep their text, at
# their own line numbers; every other line is blank. A check then runs its
# unchanged search (same patterns, globs, excludes and allowlist) from DIR, so
# its output stays "./path:line:text" with real line numbers.
#
# Renames are not followed: a renamed file counts as added, so text moved to
# another file is judged under its new name, as the filename-bound allowlists
# require. Symlinks and submodule entries are skipped, as ripgrep skips them in
# the working tree.

swarm_check_range_enabled() {
  [[ -n "${SWARM_CHECK_BASE:-}" ]]
}

swarm_check_range_label() {
  printf '%s..%s' "${SWARM_CHECK_BASE:0:12}" "${SWARM_CHECK_HEAD:-HEAD}"
}

swarm_check_range_tree() {
  local tree="$1"
  local base="${SWARM_CHECK_BASE:?SWARM_CHECK_BASE must be set}"
  local head="${SWARM_CHECK_HEAD:-HEAD}"
  local diff

  if ! git cat-file -e "${base}^{commit}" 2>/dev/null; then
    echo "[check-range] FAIL: SWARM_CHECK_BASE is not an available commit: ${base}" >&2
    return 1
  fi
  if ! git cat-file -e "${head}^{commit}" 2>/dev/null; then
    echo "[check-range] FAIL: SWARM_CHECK_HEAD is not an available commit: ${head}" >&2
    return 1
  fi

  # ripgrep would apply an enclosing repository's ignore rules to the tree and
  # silently skip the added lines; refuse such a location instead.
  if git -C "${tree}" rev-parse --git-dir >/dev/null 2>&1; then
    echo "[check-range] FAIL: ${tree} is inside a Git repository; set TMPDIR outside it" >&2
    return 1
  fi

  if ! diff="$(git -c core.quotePath=false diff -U0 --no-color --no-ext-diff --no-renames --no-relative \
    --src-prefix=a/ --dst-prefix=b/ --diff-filter=AM "${base}" "${head}")"; then
    echo "[check-range] FAIL: git diff failed for $(swarm_check_range_label)" >&2
    return 1
  fi
  [[ -n "${diff}" ]] || return 0
  if ! printf '%s\n' "${diff}" | awk -v tree="${tree}" '
    function shell_quote(s) { gsub(/\047/, "\047\\\047\047", s); return "\047" s "\047" }
    function close_current() { if (out != "") { close(out); out = "" } }
    /^diff --git / { close_current(); header = 1; skip = 0; path = ""; next }
    header && /^(new file mode|new mode|index [0-9a-f]+\.\.[0-9a-f]+) (120000|160000)$/ { skip = 1; next }
    header && /^\+\+\+ / {
      path = substr($0, 5)
      if (path ~ /^"/) { print "[check-range] cannot scope a path git quotes: " path > "/dev/stderr"; exit 3 }
      if (path !~ /^b\//) { path = ""; next }
      path = substr(path, 3)
      next
    }
    /^@@ / {
      header = 0
      if (skip || path == "") { line = -1; next }
      if (out == "") {
        out = tree "/" path
        dir = out; sub(/\/[^\/]*$/, "", dir)
        if (system("mkdir -p " shell_quote(dir)) != 0) exit 4
        printf "" > out
        written = 0
      }
      split($3, range, ",")
      line = substr(range[1], 2) + 0
      next
    }
    header { next }
    /^\+/ {
      if (line < 0) next
      while (written < line - 1) { print "" > out; written++ }
      print substr($0, 2) > out
      written = line
      line++
      next
    }
  '; then
    echo "[check-range] FAIL: could not collect the lines added in $(swarm_check_range_label)" >&2
    return 1
  fi
}
