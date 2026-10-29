#!/usr/bin/env bash
# paks/moonlight/fetch-keyboard.sh — on-screen keyboard of the Moonlight pak
# (IP entry): NextUI H700 binary of josegonzalez/minui-keyboard (MIT), pinned
# version and checksum. Output: build/cache/minui-keyboard-<version>/minui-keyboard.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
KB_VERSION="0.10.1"
KB_SHA256="97f5f85146c0f582e802550fbf598145f548d34dce6ae593bc7ef116cd0e08b6"
dir="$ROOT/build/cache/minui-keyboard-$KB_VERSION"
mkdir -p "$dir"
[ -s "$dir/minui-keyboard" ] || curl -fsSL -o "$dir/minui-keyboard" \
  "https://github.com/josegonzalez/minui-keyboard/releases/download/$KB_VERSION/minui-keyboard-h700-nextui"
sum="$(shasum -a 256 "$dir/minui-keyboard" 2>/dev/null || sha256sum "$dir/minui-keyboard")"
[ "${sum%% *}" = "$KB_SHA256" ] ||
  { rm -f "$dir/minui-keyboard"; echo "ERROR: unexpected checksum for minui-keyboard $KB_VERSION" >&2; exit 1; }
chmod 755 "$dir/minui-keyboard"
echo "$dir/minui-keyboard"
