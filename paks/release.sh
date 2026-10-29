#!/usr/bin/env bash
# paks/release.sh — build the zip of each published pak (paks/<dir>/MANIFEST).
#
#   paks/release.sh <out-dir> [dir…]    default: every pak with a MANIFEST
#
# Output: <out-dir>/<dir>.pak.zip. Go binaries are built for Linux ARM64
# (static, stdlib only). Used by ci.sh; also handy to inspect a zip locally.
set -euo pipefail
HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
OUT="${1:?usage: release.sh <out-dir> [dir…]}"; shift
[ $# -gt 0 ] || set -- $(cd "$HERE" && for m in */MANIFEST; do dirname "$m"; done)
mkdir -p "$OUT"; OUT="$(cd "$OUT" && pwd)"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/paks.XXXXXX")"; trap 'rm -rf "$tmp"' EXIT

for d in "$@"; do
  [ -f "$HERE/$d/MANIFEST" ] || { echo "ERROR: $d has no MANIFEST" >&2; exit 1; }
  stage="$tmp/$d"; mkdir -p "$stage"
  while read -r dest src; do
    case "$dest" in '' | \#*) continue ;; esac
    mkdir -p "$stage/$(dirname "$dest")"
    case "$src" in
      go:*) (cd "$HERE/${src#go:}" && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
              go build -trimpath -ldflags "-s -w" -o "$stage/$dest" .) ;;
      *) cp "$HERE/$src" "$stage/$dest" ;;
    esac
  done < "$HERE/$d/MANIFEST"
  rm -f "$OUT/$d.pak.zip"
  (cd "$stage" && find . -type f | sed 's#^\./##' | LC_ALL=C sort | zip -q -X "$OUT/$d.pak.zip" -@)
  echo "→ $d.pak.zip ($(cd "$stage" && find . -type f | wc -l | tr -d ' ') files)"
done
