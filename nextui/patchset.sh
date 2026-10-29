#!/usr/bin/env bash
# patchset.sh — empreinte (16 caractères) de ce qui fait nos binaires NextUI :
# build.sh et tous les patches (chemin et contenu). Écrite dans PATCHSET de
# chaque réserve (build.sh) et dans les notes de chaque release du dépôt public
# (ci.sh) : une release dont l'empreinte diffère est recompilée.
set -euo pipefail
HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
sha() { if command -v sha256sum >/dev/null; then sha256sum; else shasum -a 256; fi; }
cd "$HERE"
{
  for f in build.sh $(find patches -name '*.patch' | LC_ALL=C sort); do
    printf '%s\n' "$f"; cat "$f"
  done
} | sha | cut -c1-16
