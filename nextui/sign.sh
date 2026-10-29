#!/usr/bin/env bash
# sign.sh — Ed25519 signature of a file, for GitHub Actions: writes <file>.sig
# (64 raw bytes). The private key (PEM) comes from the MODS_SIGNING_KEY
# secret; the console checks with the public key in nextui/MODS_PUBKEY.
#
#   nextui/sign.sh <file>…
set -euo pipefail
: "${MODS_SIGNING_KEY:?MODS_SIGNING_KEY secret missing: nothing is published unsigned}"
key="$(mktemp "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/sign.XXXXXX")"; trap 'rm -f "$key"' EXIT
printf '%s\n' "$MODS_SIGNING_KEY" > "$key"
for f in "$@"; do
  openssl pkeyutl -sign -rawin -inkey "$key" -in "$f" -out "$f.sig"
done
