#!/usr/bin/env bash
# ci.sh — run by GitHub Actions in the public NextUI mods repo
# (.github/workflows/nextui-mods.yml, copied by mac/publish-nextui-mods.sh).
#
#   nextui/ci.sh [--force] [tag…]     default: latest and the TARGET version
#
# For each NextUI release: build our binaries (build.sh, Docker) and publish a
# release named after the build ID (e.g. NextUI-20261013-0, line 1 of
# .system/version.txt on the console):
#
#   nextui-mods-<build>.tar   the reserve as-is (flat files)
#   SHA256SUMS                its checksum
#
# The console (nextui-mods/fetch.sh) downloads it when that version comes out,
# checks the checksum against the one GitHub reports, then stores it in its
# reserve: the guard installs the binaries on the first boot of the new
# version. A release already published with the same patch hash (patchset.sh)
# is not rebuilt; otherwise its files are replaced. A patch that no longer
# applies fails the workflow (GitHub email): nothing is published and the
# console stays "NOT PREPARED".
set -euo pipefail
HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
ROOT="$(dirname "$HERE")"
FILES=(nextui.elf minarch.elf clock.elf battery.elf settings.elf minui-list minui-presenter SOURCE PATCHSET)
FORCE=no
[ "${1:-}" = --force ] && { FORCE=yes; shift; }
[ $# -gt 0 ] || set -- latest "$(tr -d '\r\n' < "$HERE/TARGET")"
: "${GH_TOKEN:?GH_TOKEN missing}"
export GITHUB_TOKEN="$GH_TOKEN" CONTAINER_RUNTIME="${CONTAINER_RUNTIME:-docker}" NEXTUI_FETCH_DEPTH=1
export NEXTUI_SRC="${NEXTUI_SRC:-${RUNNER_TEMP:-/tmp}/nextui-src}"
[ -d "$NEXTUI_SRC/.git" ] || git init -q "$NEXTUI_SRC"

PATCHSET="$("$HERE/patchset.sh")"
api() { curl -sf -H "Authorization: Bearer $GH_TOKEN" "https://api.github.com/repos/LoveRetro/NextUI/releases/$1"; }
done_ids=" "

for want in "$@"; do
  tag="$want"
  [ "$tag" = latest ] && tag="$(api latest | sed -n 's/^ *"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
  [ -n "$tag" ] || { echo "ERROR: NextUI release not found ($want)" >&2; exit 1; }
  id="$(api "tags/$tag" | sed -n 's/^ *"name": *"\(NextUI-[0-9]*-[0-9]*\)-.*/\1/p' | head -1)"
  [ -n "$id" ] || { echo "ERROR: build ID not found for $tag" >&2; exit 1; }
  case "$done_ids" in *" $id "*) continue ;; esac
  done_ids="$done_ids$id "

  published="$(gh release view "$id" --json body -q .body 2>/dev/null | sed -n 's/^patchset: //p' || true)"
  if [ "$FORCE" = no ] && [ "$published" = "$PATCHSET" ]; then
    echo "→ $tag ($id): already published with patches $PATCHSET"
    continue
  fi

  echo "→ $tag ($id): building (patches $PATCHSET)"
  "$HERE/build.sh" "$tag"
  out="$ROOT/build/nextui/$id"
  for f in "${FILES[@]}"; do [ -s "$out/$f" ] || { echo "ERROR: $f missing from $out" >&2; exit 1; }; done
  [ "$(cut -d' ' -f1 "$out/SOURCE")" = "$tag" ] || { echo "ERROR: SOURCE does not match $tag" >&2; exit 1; }

  dist="$ROOT/build/dist/$id"
  rm -rf "$dist"; mkdir -p "$dist"
  tar -cf "$dist/nextui-mods-$id.tar" -C "$out" "${FILES[@]}"
  (cd "$dist" && sha256sum "nextui-mods-$id.tar" > SHA256SUMS)
  notes="$(printf 'NextUI %s (%s), commit %s\n\npatchset: %s\nbuilt: %s\n' \
    "$tag" "$id" "$(cut -d' ' -f2 "$out/SOURCE")" "$PATCHSET" "${GITHUB_SHA:-local}")"

  if gh release view "$id" >/dev/null 2>&1; then
    gh release upload "$id" "$dist/nextui-mods-$id.tar" "$dist/SHA256SUMS" --clobber
    gh release edit "$id" --notes "$notes"
  else
    gh release create "$id" "$dist/nextui-mods-$id.tar" "$dist/SHA256SUMS" \
      --title "$tag ($id)" --notes "$notes" ${GITHUB_SHA:+--target "$GITHUB_SHA"}
  fi
  echo "→ published: $id ($(cut -d' ' -f1 "$dist/SHA256SUMS" | cut -c1-16))"
done
