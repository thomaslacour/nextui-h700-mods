#!/usr/bin/env bash
# moonlight/build.sh — prépare le client Moonlight du pak Moonlight (H700).
#
# Le binaire moonlight-embedded vient du port PortMaster « Moonlight New »
# (zip figé, md5 publié par PortMaster vérifié). Il suppose des bibliothèques
# que BaseOS n'a pas ; on les compile ici dans la toolchain NextUI (glibc 2.28) :
#   libevdev.so.2              manette (source officielle freedesktop)
#   libnghttp2.so.14           requise par le libcurl du port
#   libavahi-{client,common}   bouchon (avahi-stub.c) : pas de découverte mDNS
#   libunbuffer.so             LD_PRELOAD de « pair » : PIN lisible tout de suite
# Le reste (ffmpeg, SDL2, opus, pulse…) est déjà sur la console
# (.system/h700/lib, PORTS.pak/lib) ou fourni par le port.
#
# Sortie : build/moonlight/ (bin/moonlight, lib/*.so*, licenses/, SOURCE),
# déposée par mac/deploy.sh dans Tools/h700/Moonlight.pak/.
set -euo pipefail

HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
OUT="$(dirname "$HERE")/build/moonlight"
CACHE="$(dirname "$HERE")/build/cache"
IMAGE="${NEXTUI_IMAGE:-ghcr.io/loveretro/h700-toolchain:latest}"

PORT_URL="https://github.com/PortsMaster/PortMaster-New/releases/download/2024-07-02_1342/moonlightnew.zip"
PORT_MD5="3d02c38b8feef63e050678214d29ff15"           # ports.json de PortMaster
EVDEV_URL="https://www.freedesktop.org/software/libevdev/libevdev-1.13.3.tar.xz"
NGHTTP2_URL="https://github.com/nghttp2/nghttp2/releases/download/v1.64.0/nghttp2-1.64.0.tar.xz"
# Empreintes SHA-256 relevées au premier téléchargement (20/10/2026).
PORT_SHA="4ec931b976f77b46bcc0b2ba0cb923e95deb83286adf19ba14abbf7c83c7c27c"
EVDEV_SHA="abf1aace86208eebdd5d3550ffded4c8d73bb405b796d51c389c9d0604cbcfbf"
NGHTTP2_SHA="88bb94c9e4fd1c499967f83dece36a78122af7d5fb40da2019c56b9ccc6eb9dd"

die() { echo "ERROR: $*" >&2; exit 1; }
sha() { shasum -a 256 "$1" | cut -d' ' -f1; }

# fetch <url> <sha attendu ou vide> : télécharge dans le cache, vérifie.
fetch() {
  f="$CACHE/$(basename "$1")"
  if [ ! -s "$f" ]; then curl -fsSL --retry 2 -o "$f.part" "$1" && mv "$f.part" "$f"; fi
  got="$(sha "$f")"
  if [ -n "$2" ] && [ "$got" != "$2" ]; then rm -f "$f"; die "empreinte inattendue pour $(basename "$f")"; fi
  echo "  $(basename "$f")  sha256=$got"
}

RUNTIME="${CONTAINER_RUNTIME:-}"
[ -n "$RUNTIME" ] || for r in container podman; do command -v "$r" >/dev/null 2>&1 && { RUNTIME="$r"; break; }; done
[ -n "$RUNTIME" ] || die "neither container (Apple) nor podman (or set CONTAINER_RUNTIME)"

mkdir -p "$CACHE"
echo "→ Sources"
fetch "$PORT_URL" "$PORT_SHA"
[ "$(md5 -q "$CACHE/moonlightnew.zip" 2>/dev/null || md5sum "$CACHE/moonlightnew.zip" | cut -d' ' -f1)" = "$PORT_MD5" ] ||
  die "md5 du port différent de celui publié par PortMaster"
fetch "$EVDEV_URL" "$EVDEV_SHA"
fetch "$NGHTTP2_URL" "$NGHTTP2_SHA"

WORK="$(mktemp -d "$CACHE/../moonlight-work.XXXXXX")"   # sous build/ : visible du conteneur
# Docker on Linux (CI) leaves root-owned files: remove them through the image.
trap 'rm -rf "$WORK" 2>/dev/null || "$RUNTIME" run --rm -v "$WORK":/root/w "$IMAGE" /bin/sh -c "rm -rf /root/w/*" >/dev/null 2>&1; rm -rf "$WORK" 2>/dev/null || true' EXIT
cp "$CACHE/$(basename "$EVDEV_URL")" "$CACHE/$(basename "$NGHTTP2_URL")" "$HERE/avahi-stub.c" "$HERE/unbuffer.c" "$WORK/"
unzip -q "$CACHE/moonlightnew.zip" 'moonlightnew/moonlight/*' 'moonlightnew/licenses/*' -d "$WORK/port"

echo "→ Compilation ($IMAGE, $RUNTIME)"
"$RUNTIME" run --rm -v "$WORK":/root/w "$IMAGE" /bin/bash -euc '
  cd /root/w; mkdir -p out
  export CC=${CROSS_COMPILE}gcc
  H=--host=aarch64-nextui-linux-gnu
  tar xf libevdev-*.tar.xz && cd libevdev-*/
  ./configure $H --prefix=/usr --disable-static --disable-gcov >/dev/null
  make -j"$(nproc)" -C libevdev >/dev/null
  cp -L libevdev/.libs/libevdev.so.2 ../out/; cd ..
  tar xf nghttp2-*.tar.xz && cd nghttp2-*/
  ./configure $H --prefix=/usr --enable-lib-only --disable-static >/dev/null
  make -j"$(nproc)" -C lib >/dev/null
  cp -L lib/.libs/libnghttp2.so.14 ../out/; cd ..
  $CC -shared -fPIC -O2 -Wall -Wl,-soname,libavahi-client.so.3 -o out/libavahi-client.so.3 avahi-stub.c
  $CC -shared -fPIC -O2 -Wall -Wl,-soname,libavahi-common.so.3 -o out/libavahi-common.so.3 avahi-stub.c
  $CC -shared -fPIC -O2 -Wall -o out/libunbuffer.so unbuffer.c
  ${CROSS_COMPILE}strip out/*.so*
'

rm -rf "$OUT"; mkdir -p "$OUT/bin" "$OUT/lib" "$OUT/licenses"
cp "$WORK/port/moonlightnew/moonlight/moonlight" "$OUT/bin/"
cp "$WORK/port/moonlightnew/moonlight/libs/"* "$WORK/out/"* "$OUT/lib/"
# Licences utiles au client (pas celles du lanceur LÖVE ni de ses musiques).
cp "$WORK/port/moonlightnew/licenses/LICENSE.moonlight.txt" "$WORK/port/moonlightnew/licenses/CREDITS AND THANKS.txt" "$OUT/licenses/"
{
  echo "moonlight: $PORT_URL (md5 $PORT_MD5)"
  echo "libevdev: $EVDEV_URL"
  echo "nghttp2: $NGHTTP2_URL"
  echo "avahi: stub (moonlight/avahi-stub.c)"
  echo "unbuffer: moonlight/unbuffer.c"
} > "$OUT/SOURCE"
echo "MOONLIGHT-BUILD-OK -> $OUT"
ls "$OUT/lib"
