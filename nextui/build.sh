#!/usr/bin/env bash
# nextui/build.sh — compile les binaires NextUI patchés pour la RG35XX SP (H700),
# pour UNE version officielle de NextUI (LoveRetro/NextUI, par tag de release).
#
#   ./build.sh                 version de nextui/TARGET (celle de la console)
#   ./build.sh v6.16.0         une autre version (préparer la prochaine)
#   ./build.sh latest          la dernière release publiée
#   ./build.sh --check [tag]   patches applicables, sans compiler
#
# Sortie : build/nextui/<build NextUI>/ (ex. NextUI-20261013-0, l'identifiant
# de la ligne 1 de .system/version.txt sur la console), avec :
#   nextui.elf minarch.elf clock.elf battery.elf   cadran, MENU -> Tools
#   settings.elf                                   Tools Settings : même cadran
#   minui-list minui-presenter                     briques des paks
#   SOURCE                                         tag et commit compilés
#   PATCHSET                                       empreinte des patches et de ce script
# mac/install-nextui.sh dépose chaque dossier dans la réserve de la console ;
# au démarrage, guard.sh installe celui qui correspond à la version présente.
#
# Aussi lancé par GitHub Actions (nextui/ci.sh, dépôt public NextUI mods) :
# GITHUB_TOKEN évite la limite de l'API, NEXTUI_FETCH_DEPTH=1 évite de cloner tout NextUI.
#
# Compilation dans un worktree git jetable (la copie NextUI n'est jamais
# modifiée ; le tag y est seulement récupéré s'il manque) et un conteneur.
set -euo pipefail

HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
SRC="${NEXTUI_SRC:-$HOME/Downloads/NextUI-h700-source}"
REPO="LoveRetro/NextUI"
IMAGE="${NEXTUI_IMAGE:-ghcr.io/loveretro/h700-toolchain:latest}"
# Versions figées des briques (patches/minui-list/ s'applique à celle-ci).
LIST_REF="${MINUI_LIST_REF:-0d415fa}"
PRESENTER_REF="${MINUI_PRESENTER_REF:-418e535}"
die() { echo "ERROR: $*" >&2; exit 1; }
CHECK=no
[ "${1:-}" = --check ] && { CHECK=yes; shift; }
TAG="${1:-$(tr -d '\r\n' < "$HERE/TARGET")}"
api() { curl -sf ${GITHUB_TOKEN:+-H "Authorization: Bearer $GITHUB_TOKEN"} "https://api.github.com/repos/$REPO/releases/$1"; }
[ "$TAG" = latest ] && TAG="$(api latest | sed -n 's/^ *"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
[ -n "$TAG" ] || die "release introuvable sur $REPO"
# Identifiant de build (= ligne 1 de version.txt) : préfixe des fichiers de la release.
ID="$(api "tags/$TAG" | sed -n 's/^ *"name": *"\(NextUI-[0-9]*-[0-9]*\)-.*/\1/p' | head -1)"
[ -n "$ID" ] || die "identifiant de build introuvable pour $TAG (fichiers NextUI-<date>-<n>-*.zip)"
OUT="$(dirname "$HERE")/build/nextui/$ID"

PATCHES=("$HERE"/patches/*.patch)
[ -s "${PATCHES[0]}" ] || die "aucun patch dans $HERE/patches"
command -v git >/dev/null || die "git introuvable"
[ -d "$SRC/.git" ] || die "dépôt NextUI introuvable : $SRC (NEXTUI_SRC=…)"
git -C "$SRC" cat-file -e "$TAG^{commit}" 2>/dev/null ||
  git -C "$SRC" fetch --quiet ${NEXTUI_FETCH_DEPTH:+--depth "$NEXTUI_FETCH_DEPTH"} "https://github.com/$REPO" tag "$TAG" || die "tag $TAG introuvable sur $REPO"
BASE="$(git -C "$SRC" rev-parse --short=8 "$TAG^{commit}")"

# Apple container d'abord (choix du propriétaire), docker/podman en secours.
RUNTIME="${CONTAINER_RUNTIME:-}"
if [ -z "$RUNTIME" ]; then
  for r in container docker podman; do command -v "$r" >/dev/null 2>&1 && { RUNTIME="$r"; break; }; done
fi
[ -n "$RUNTIME" ] || die "ni container (Apple), ni docker, ni podman dans le PATH"

echo "→ NextUI         : $TAG ($ID, commit $BASE)"
echo "→ Toolchain      : $IMAGE ($RUNTIME)"

# Atelier jetable sous $HOME (les VM de conteneurs ne partagent que lui).
mkdir -p "$HOME/.cache"
WORK="$(mktemp -d "$HOME/.cache/save-sync-nextui.XXXXXX")"
cleanup() { git -C "$SRC" worktree remove --force "$WORK/nextui" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

git -C "$SRC" worktree add --quiet --detach "$WORK/nextui" "$BASE"
for p in "${PATCHES[@]}"; do
  echo "→ patch $(basename "$p")"
  git -C "$WORK/nextui" apply --whitespace=nowarn "$p" || die "$(basename "$p") ne s'applique pas au commit $BASE"
done

if [ "$CHECK" = yes ]; then
  for p in "$HERE"/patches/bricks/*.patch; do
    git -C "$WORK/nextui" apply --check --whitespace=nowarn "$p" || die "$(basename "$p") ne s'applique pas"
  done
  "$RUNTIME" image inspect "$IMAGE" >/dev/null 2>&1 && echo "→ image présente" || echo "⚠ image absente : $RUNTIME image pull $IMAGE"
  echo "Vérifications OK (aucune compilation)."
  exit 0
fi

"$RUNTIME" image inspect "$IMAGE" >/dev/null 2>&1 || "$RUNTIME" image pull "$IMAGE"

git -C "$SRC" rev-parse --short "$TAG^{commit}" > "$WORK/nextui/workspace/hash.txt"
for pair in "minui-list|$LIST_REF" "minui-presenter|$PRESENTER_REF"; do
  b="${pair%%|*}"
  git clone --quiet "https://github.com/josegonzalez/$b" "$WORK/$b"
  git -C "$WORK/$b" checkout --quiet "${pair##*|}"
  for p in "$HERE/patches/$b"/*.patch; do
    [ -e "$p" ] || continue
    echo "→ patch $b $(basename "$p")"
    git -C "$WORK/$b" apply --whitespace=nowarn "$p" || die "$(basename "$p") ne s'applique pas à $b ${pair##*|}"
  done
  git clone --quiet --depth 1 https://github.com/kgabis/parson "$WORK/$b/include/parson"
  ln -sfn ../nextui "$WORK/$b/minui"
done

in_toolchain() { "$RUNTIME" run --rm -v "$WORK":/root/w -e PLATFORM=h700 -e UNION_PLATFORM=h700 "$IMAGE" /bin/bash -euc "$1"; }

echo "→ Compilation du launcher, de minarch, de Clock, Battery et Settings…"
in_toolchain 'ws=/root/w/nextui/workspace
  make -C "$ws/h700/libmsettings" && make -C "$ws/all/libbatmondb" && make -C "$ws/all/nextui" &&
  make -C "$ws/all/minarch" && make -C "$ws/all/clock" && make -C "$ws/all/battery" && make -C "$ws/all/settings"'

# Les briques compilent la même source, plus patches/bricks/ : leur sortie
# standard est lue par les paks, aucun journal ne doit y passer.
for p in "$HERE"/patches/bricks/*.patch; do
  echo "→ patch briques $(basename "$p")"
  git -C "$WORK/nextui" apply --whitespace=nowarn "$p" || die "$(basename "$p") ne s'applique pas"
done
echo "→ Compilation des briques…"
in_toolchain 'make -C /root/w/nextui/workspace/h700/libmsettings
  for b in minui-list minui-presenter; do
    mkdir -p "/root/w/$b/platform/h700-nextui/lib" "/root/w/$b/platform/h700-nextui/include"
    make -C "/root/w/$b" PLATFORM=h700-nextui
  done'

mkdir -p "$OUT"
for b in nextui minarch clock battery settings; do
  install -m 755 "$WORK/nextui/workspace/all/$b/build/h700/$b.elf" "$OUT/$b.elf"
done
for b in minui-list minui-presenter; do install -m 755 "$WORK/$b/$b-h700-nextui" "$OUT/$b"; done
printf '%s %s\n' "$TAG" "$(git -C "$SRC" rev-parse "$TAG^{commit}")" > "$OUT/SOURCE"
"$HERE/patchset.sh" > "$OUT/PATCHSET"

echo
echo "OK -> $OUT"
for f in "$OUT"/*; do printf '  %-16s %s\n' "$(basename "$f")" "$(shasum -a 256 "$f" | cut -c1-16)"; done
echo "Réserve de la console : ../mac/install-nextui.sh"
