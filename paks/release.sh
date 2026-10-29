#!/usr/bin/env bash
# paks/release.sh — build the zip of each published pak (paks/<dir>/MANIFEST).
#
#   paks/release.sh <out-dir> [dir…]    default: every pak with a MANIFEST
#
# MANIFEST lines, paths from the repository root:
#   <path in the pak>  <file>           copy a file
#   <path in the pak>/ <folder>/        copy a folder's files
#   <path in the pak>  go:<folder>      Go program (Linux ARM64, static)
#   @run <command…>                     run first (container builds into build/)
# Output: <out-dir>/<dir>.pak.zip.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:?usage: release.sh <out-dir> [dir…]}"; shift
[ $# -gt 0 ] || set -- $(cd "$ROOT/paks" && for m in */MANIFEST; do dirname "$m"; done)
mkdir -p "$OUT"; OUT="$(cd "$OUT" && pwd)"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/paks.XXXXXX")"; trap 'rm -rf "$tmp"' EXIT
cd "$ROOT"

for d in "$@"; do
  m="paks/$d/MANIFEST"
  [ -f "$m" ] || { echo "ERROR: $m missing" >&2; exit 1; }
  stage="$tmp/$d"; mkdir -p "$stage"
  while read -r dest src; do
    case "$dest" in '' | \#*) continue ;; @run) bash -c "$src"; continue ;; esac
    case "$src" in
      go:*) (cd "${src#go:}" && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
              go build -trimpath -ldflags "-s -w" -o "$stage/$dest" .) ;;
      */) mkdir -p "$stage/$dest" && cp -R "$src". "$stage/$dest" ;;
      *) mkdir -p "$stage/$(dirname "$dest")" && cp "$src" "$stage/$dest" ;;
    esac
  done < "$m"
  rm -f "$OUT/$d.pak.zip"
  (cd "$stage" && find . -type f | sed 's#^\./##' | LC_ALL=C sort | zip -q -X "$OUT/$d.pak.zip" -@)
  echo "→ $d.pak.zip ($(cd "$stage" && find . -type f | wc -l | tr -d ' ') files)"
done
