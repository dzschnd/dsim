#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CWD="$(pwd)"

resolve_dir() {
  local p="$1"
  if [[ -d "$p" ]]; then
    cd "$p" && pwd
  elif [[ -d "$CWD/$p" ]]; then
    cd "$CWD/$p" && pwd
  elif [[ -d "$ROOT/$p" ]]; then
    cd "$ROOT/$p" && pwd
  else
    echo "error: directory not found: $p" >&2
    exit 1
  fi
}

if [[ -n "${1:-}" ]]; then
  SCAN_DIR="$(resolve_dir "$1")"
else
  SCAN_DIR="$ROOT"
fi

if [[ -n "${2:-}" ]]; then
  out_dir="$(dirname "$2")"
  out_base="$(basename "$2")"
  if [[ -d "$out_dir" ]]; then
    OUTPUT="$(cd "$out_dir" && pwd)/$out_base"
  elif [[ -d "$CWD/$out_dir" ]]; then
    OUTPUT="$(cd "$CWD/$out_dir" && pwd)/$out_base"
  else
    OUTPUT="$CWD/$2"
  fi
else
  OUTPUT="$ROOT/project_source.txt"
fi

> "$OUTPUT"

if [[ "$SCAN_DIR" == "$ROOT" ]]; then
  mapfile -t files < <(
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
    | sort
  )
else
  mapfile -t files < <(find "$SCAN_DIR" -type f | sort)
fi

for file in "${files[@]}"; do
  [[ "$file" == "$OUTPUT" ]] && continue
  grep -qI '' "$file" 2>/dev/null || continue
  rel="${file#${SCAN_DIR}/}"
  printf '%s:\n' "$rel"
  cat "$file"
  printf '\n'
done >> "$OUTPUT"

echo "Written to $OUTPUT"
