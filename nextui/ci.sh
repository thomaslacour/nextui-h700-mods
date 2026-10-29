#!/usr/bin/env bash
# ci.sh — lancé par GitHub Actions dans le dépôt public NextUI mods
# (.github/workflows/nextui-mods.yml, copié par mac/publish-nextui-mods.sh).
#
#   nextui/ci.sh [--force] [tag…]     défaut : latest et la version de TARGET
#
# Pour chaque version de NextUI : compile nos binaires (build.sh, Docker) et
# publie une release nommée d'après l'identifiant de build (ex.
# NextUI-20261013-0, ligne 1 de .system/version.txt sur la console) :
#
#   nextui-mods-<build>.tar   la réserve telle quelle (fichiers à plat)
#   SHA256SUMS                son empreinte
#
# La console (nextui-mods/fetch.sh) la télécharge quand cette version sort,
# vérifie l'empreinte contre celle que GitHub annonce, puis la range dans sa
# réserve : la garde pose les binaires au premier démarrage de la nouvelle
# version. Une release déjà publiée avec la même empreinte de patches
# (patchset.sh) n'est pas recompilée ; sinon ses fichiers sont remplacés.
# Un patch qui ne s'applique plus fait échouer le workflow (courriel de
# GitHub) : rien n'est publié, la console reste « NOT PREPARED ».
set -euo pipefail
HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
ROOT="$(dirname "$HERE")"
FILES=(nextui.elf minarch.elf clock.elf battery.elf settings.elf minui-list minui-presenter SOURCE PATCHSET)
FORCE=no
[ "${1:-}" = --force ] && { FORCE=yes; shift; }
[ $# -gt 0 ] || set -- latest "$(tr -d '\r\n' < "$HERE/TARGET")"
: "${GH_TOKEN:?GH_TOKEN manquant}"
export GITHUB_TOKEN="$GH_TOKEN" CONTAINER_RUNTIME="${CONTAINER_RUNTIME:-docker}" NEXTUI_FETCH_DEPTH=1
export NEXTUI_SRC="${NEXTUI_SRC:-${RUNNER_TEMP:-/tmp}/nextui-src}"
[ -d "$NEXTUI_SRC/.git" ] || git init -q "$NEXTUI_SRC"

PATCHSET="$("$HERE/patchset.sh")"
api() { curl -sf -H "Authorization: Bearer $GH_TOKEN" "https://api.github.com/repos/LoveRetro/NextUI/releases/$1"; }
done_ids=" "

for want in "$@"; do
  tag="$want"
  [ "$tag" = latest ] && tag="$(api latest | sed -n 's/^ *"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
  [ -n "$tag" ] || { echo "ERROR: release NextUI introuvable ($want)" >&2; exit 1; }
  id="$(api "tags/$tag" | sed -n 's/^ *"name": *"\(NextUI-[0-9]*-[0-9]*\)-.*/\1/p' | head -1)"
  [ -n "$id" ] || { echo "ERROR: identifiant de build introuvable pour $tag" >&2; exit 1; }
  case "$done_ids" in *" $id "*) continue ;; esac
  done_ids="$done_ids$id "

  published="$(gh release view "$id" --json body -q .body 2>/dev/null | sed -n 's/^patchset: //p' || true)"
  if [ "$FORCE" = no ] && [ "$published" = "$PATCHSET" ]; then
    echo "→ $tag ($id) : déjà publiée avec les patches $PATCHSET"
    continue
  fi

  echo "→ $tag ($id) : compilation (patches $PATCHSET)"
  "$HERE/build.sh" "$tag"
  out="$ROOT/build/nextui/$id"
  for f in "${FILES[@]}"; do [ -s "$out/$f" ] || { echo "ERROR: $f absent de $out" >&2; exit 1; }; done
  [ "$(cut -d' ' -f1 "$out/SOURCE")" = "$tag" ] || { echo "ERROR: SOURCE ne correspond pas à $tag" >&2; exit 1; }

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
  echo "→ publiée : $id ($(cut -d' ' -f1 "$dist/SHA256SUMS" | cut -c1-16))"
done
