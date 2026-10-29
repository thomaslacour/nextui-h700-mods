#!/usr/bin/env bash
# paks/pad-setup/probe/build.sh — build pad-probe (reads the pad through
# NextUI's SDL2) in the H700 toolchain container. Output: build/pad-setup/.
# Runtime: CONTAINER_RUNTIME, else Apple container, else podman.
set -euo pipefail
HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
OUT="$(cd "$HERE/../../.." && pwd)/build/pad-setup"   # under $HOME: visible to the container
RUNTIME="${CONTAINER_RUNTIME:-}"
[ -n "$RUNTIME" ] || for r in container podman; do command -v "$r" >/dev/null 2>&1 && { RUNTIME="$r"; break; }; done
[ -n "$RUNTIME" ] || { echo "ERROR: neither container (Apple) nor podman" >&2; exit 1; }
mkdir -p "$OUT"; cp "$HERE/pad-probe.c" "$OUT/"
"$RUNTIME" run --rm -v "$OUT":/root/w "${NEXTUI_IMAGE:-ghcr.io/loveretro/h700-toolchain:latest}" /bin/bash -euc '
  cd /root/w && ${CROSS_COMPILE}gcc -O2 -Wall -Wextra -Werror -s -I/opt/nextui/include -o pad-probe pad-probe.c -lSDL2'
echo "OK -> build/pad-setup/pad-probe"
