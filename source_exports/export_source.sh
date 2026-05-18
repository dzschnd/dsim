#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SCAN_DIR="$(cd "${1:-.}" && pwd)"
OUTPUT="${2:-${ROOT}/project_source.txt}"

> "$OUTPUT"

find "$SCAN_DIR" \
  \( \
    -name ".agents" \
    -o -name ".claude" \
    -o -name ".git" \
    -o -name ".gocache" \
    -o -name "tmp" \
    -o -name "dist" \
    -o -name "node_modules" \
    -o -name "scripts" \
    -o -name "topo-examples" \
  \) -prune \
  -o -name ".env" -prune \
  -o -type f -print \
  | sort \
  | while IFS= read -r file; do
    [[ "$file" == "$OUTPUT" ]] && continue
    grep -qI '' "$file" 2>/dev/null || continue
    rel="${file#${SCAN_DIR}/}"
    printf '%s:\n' "$rel"
    cat "$file"
    printf '\n'
  done >> "$OUTPUT"

echo "Written to $OUTPUT"
