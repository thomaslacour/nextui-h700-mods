#!/usr/bin/env bash
# patchset.sh — 16-char hash of what makes our NextUI binaries: build.sh and
# every patch (path and content). Written to PATCHSET in each reserve
# (build.sh) and to the notes of each release in the public repo (ci.sh): a
# release whose hash differs is rebuilt.
set -euo pipefail
HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
sha() { if command -v sha256sum >/dev/null; then sha256sum; else shasum -a 256; fi; }
cd "$HERE"
{
  for f in build.sh $(find patches -name '*.patch' | LC_ALL=C sort); do
    printf '%s\n' "$f"; cat "$f"
  done
} | sha | cut -c1-16
