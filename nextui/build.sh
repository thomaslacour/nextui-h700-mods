#!/usr/bin/env bash
# nextui/build.sh — build the patched NextUI binaries for the RG35XX SP (H700),
# for ONE official NextUI release (LoveRetro/NextUI, by release tag).
#
#   ./build.sh                 the version in nextui/TARGET (the console's)
#   ./build.sh v6.16.0         another version (prepare the next one)
#   ./build.sh latest          the latest published release
#   ./build.sh --check [tag]   do the patches apply? no build
#
# Output: build/nextui/<NextUI build>/ (e.g. NextUI-20261013-0, the ID on
# line 1 of .system/version.txt on the console), with:
#   nextui.elf minarch.elf clock.elf battery.elf   status dial, MENU -> Tools
#   settings.elf                                   Tools Settings: same dial
#   minui-list minui-presenter                     pak bricks
#   SOURCE                                         tag and commit built
#   PATCHSET                                       hash of the patches and this script
# mac/install-nextui.sh copies each folder into the console's reserve; at boot,
# guard.sh installs the one matching the installed version.
#
# Also run by GitHub Actions (nextui/ci.sh, public NextUI mods repo):
# GITHUB_TOKEN avoids the API rate limit, NEXTUI_FETCH_DEPTH=1 avoids cloning all of NextUI.
#
# Builds in a throwaway git worktree (the NextUI clone is never changed; the
# tag is only fetched when missing) and a container.
set -euo pipefail

HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
SRC="${NEXTUI_SRC:-$(dirname "$HERE")/build/nextui-src}"
REPO="LoveRetro/NextUI"
IMAGE="${NEXTUI_IMAGE:-ghcr.io/loveretro/h700-toolchain:latest}"
# Pinned brick versions (patches/minui-list/ applies to this one).
LIST_REF="${MINUI_LIST_REF:-0d415fa}"
PRESENTER_REF="${MINUI_PRESENTER_REF:-418e535}"
die() { echo "ERROR: $*" >&2; exit 1; }
CHECK=no
[ "${1:-}" = --check ] && { CHECK=yes; shift; }
TAG="${1:-$(tr -d '\r\n' < "$HERE/TARGET")}"
api() { curl -sf ${GITHUB_TOKEN:+-H "Authorization: Bearer $GITHUB_TOKEN"} "https://api.github.com/repos/$REPO/releases/$1"; }
[ "$TAG" = latest ] && TAG="$(api latest | sed -n 's/^ *"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
[ -n "$TAG" ] || die "release not found on $REPO"
# Build ID (= line 1 of version.txt): prefix of the release files.
ID="$(api "tags/$TAG" | sed -n 's/^ *"name": *"\(NextUI-[0-9]*-[0-9]*\)-.*/\1/p' | head -1)"
[ -n "$ID" ] || die "build ID not found for $TAG (files NextUI-<date>-<n>-*.zip)"
OUT="$(dirname "$HERE")/build/nextui/$ID"

PATCHES=("$HERE"/patches/*.patch)
[ -s "${PATCHES[0]}" ] || die "no patch in $HERE/patches"
command -v git >/dev/null || die "git not found"
# Default clone: created empty, then each tag is fetched shallow.
if [ ! -d "$SRC/.git" ]; then
  [ -z "${NEXTUI_SRC:-}" ] || die "NextUI repo not found: $SRC (NEXTUI_SRC=…)"
  git init -q --template= "$SRC"; NEXTUI_FETCH_DEPTH="${NEXTUI_FETCH_DEPTH:-1}"
fi
[ -f "$SRC/.git/shallow" ] && NEXTUI_FETCH_DEPTH="${NEXTUI_FETCH_DEPTH:-1}"
git -C "$SRC" cat-file -e "$TAG^{commit}" 2>/dev/null ||
  git -C "$SRC" fetch --quiet ${NEXTUI_FETCH_DEPTH:+--depth "$NEXTUI_FETCH_DEPTH"} "https://github.com/$REPO" tag "$TAG" || die "tag $TAG not found on $REPO"
BASE="$(git -C "$SRC" rev-parse --short=8 "$TAG^{commit}")"

# Apple container first (owner's choice), podman as fallback; docker only when
# set explicitly (CONTAINER_RUNTIME=docker, GitHub Actions).
RUNTIME="${CONTAINER_RUNTIME:-}"
if [ -z "$RUNTIME" ]; then
  for r in container podman; do command -v "$r" >/dev/null 2>&1 && { RUNTIME="$r"; break; }; done
fi
[ -n "$RUNTIME" ] || die "neither container (Apple) nor podman in PATH"

echo "→ NextUI         : $TAG ($ID, commit $BASE)"
echo "→ Toolchain      : $IMAGE ($RUNTIME)"

# Throwaway workspace under $HOME (container VMs only share that).
mkdir -p "$HOME/.cache"
WORK="$(mktemp -d "$HOME/.cache/save-sync-nextui.XXXXXX")"
# On Linux (CI), Docker leaves root-owned files: remove them through the image.
cleanup() {
  git -C "$SRC" worktree remove --force "$WORK/nextui" >/dev/null 2>&1 || true
  rm -rf "$WORK" 2>/dev/null ||
    "$RUNTIME" run --rm -v "$WORK":/root/w "$IMAGE" /bin/sh -c 'rm -rf /root/w/*' >/dev/null 2>&1 || true
  rm -rf "$WORK" 2>/dev/null || true
  git -C "$SRC" worktree prune 2>/dev/null || true
}
trap cleanup EXIT

git -C "$SRC" worktree add --quiet --detach "$WORK/nextui" "$BASE"
for p in "${PATCHES[@]}"; do
  echo "→ patch $(basename "$p")"
  git -C "$WORK/nextui" apply --whitespace=nowarn "$p" || die "$(basename "$p") does not apply to commit $BASE"
done

if [ "$CHECK" = yes ]; then
  for p in "$HERE"/patches/bricks/*.patch; do
    git -C "$WORK/nextui" apply --check --whitespace=nowarn "$p" || die "$(basename "$p") does not apply"
  done
  "$RUNTIME" image inspect "$IMAGE" >/dev/null 2>&1 && echo "→ image present" || echo "⚠ image missing: $RUNTIME image pull $IMAGE"
  echo "Checks OK (nothing built)."
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
    git -C "$WORK/$b" apply --whitespace=nowarn "$p" || die "$(basename "$p") does not apply to $b ${pair##*|}"
  done
  git clone --quiet --depth 1 https://github.com/kgabis/parson "$WORK/$b/include/parson"
  ln -sfn ../nextui "$WORK/$b/minui"
done

in_toolchain() { "$RUNTIME" run --rm -v "$WORK":/root/w -e PLATFORM=h700 -e UNION_PLATFORM=h700 "$IMAGE" /bin/bash -euc "$1"; }

echo "→ Building the launcher, minarch, Clock, Battery and Settings…"
in_toolchain 'ws=/root/w/nextui/workspace
  make -C "$ws/h700/libmsettings" && make -C "$ws/all/libbatmondb" && make -C "$ws/all/nextui" &&
  make -C "$ws/all/minarch" && make -C "$ws/all/clock" && make -C "$ws/all/battery" && make -C "$ws/all/settings"'

# The bricks build the same source plus patches/bricks/: the paks read their
# stdout, so no log line may go there.
for p in "$HERE"/patches/bricks/*.patch; do
  echo "→ patch bricks $(basename "$p")"
  git -C "$WORK/nextui" apply --whitespace=nowarn "$p" || die "$(basename "$p") does not apply"
done
echo "→ Building the bricks…"
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
echo "Console reserve: ../mac/install-nextui.sh"
