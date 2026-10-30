#!/bin/sh
# post-install.sh — run by `pak` after installing or updating Updates.pak
# (from the pak folder; `pak` has already checked the new program starts).
# Installs what lives outside the pak, the way mac/deploy.sh does:
#   .userdata/<platform>/ports/port        the same program, named port
#   .userdata/<platform>/ports/port-hm.py  its bridge to the PortMaster engine
#   .hooks/boot.d/12-pak-cli, 13-port-cli  /usr/bin/pak and /usr/bin/port links
# then runs the two hooks so the links are right now, not only at next boot.
# pak CLI vit hors de Updates.pak dans /.userdata/bin/pak (robuste si désactivé)
set -e
SD="${SDCARD_PATH:-/mnt/SDCARD}"
UD="${USERDATA_PATH:-$SD/.userdata/${PLATFORM:-h700}}"
BIN_DIR="$SD/.userdata/bin"

put() { # <source> <destination>: written as .new, then renamed
    mkdir -p "$(dirname "$2")"
    cp "$1" "$2.new"
    chmod 755 "$2.new"
    mv "$2.new" "$2"
}
put pak "$UD/ports/port"
put payload/port-hm.py "$UD/ports/port-hm.py"
# Copier pak vers /.userdata/bin/pak s'il est dans payload (nouvelle forme) ou si absent
if [ -f payload/pak ]; then
    mkdir -p "$BIN_DIR"
    put payload/pak "$BIN_DIR/pak"
    ln -sf "$BIN_DIR/pak" /usr/bin/pak 2>/dev/null || true
elif [ -f pak ] && [ ! -x "$BIN_DIR/pak" ]; then
    # Ancienne forme éventuelle : garder rétrocompatible
    mkdir -p "$BIN_DIR"
    put pak "$BIN_DIR/pak"
    ln -sf "$BIN_DIR/pak" /usr/bin/pak 2>/dev/null || true
fi
for h in payload/hooks/*.sh; do
    put "$h" "$UD/.hooks/boot.d/${h##*/}"
    /bin/sh "$UD/.hooks/boot.d/${h##*/}" || true
done
sync
