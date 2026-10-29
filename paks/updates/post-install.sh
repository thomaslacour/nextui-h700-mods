#!/bin/sh
# post-install.sh — run by `pak` after installing or updating Updates.pak
# (from the pak folder; `pak` has already checked the new program starts).
# Installs what lives outside the pak, the way mac/deploy.sh does:
#   .userdata/<platform>/ports/port        the same program, named port
#   .userdata/<platform>/ports/port-hm.py  its bridge to the PortMaster engine
#   .hooks/boot.d/12-pak-cli, 13-port-cli  /usr/bin/pak and /usr/bin/port links
# then runs the two hooks so the links are right now, not only at next boot.
set -e
UD="${USERDATA_PATH:-${SDCARD_PATH:-/mnt/SDCARD}/.userdata/${PLATFORM:-h700}}"

put() { # <source> <destination>: written as .new, then renamed
    mkdir -p "$(dirname "$2")"
    cp "$1" "$2.new"
    chmod 755 "$2.new"
    mv "$2.new" "$2"
}
put pak "$UD/ports/port"
put payload/port-hm.py "$UD/ports/port-hm.py"
for h in payload/hooks/*.sh; do
    put "$h" "$UD/.hooks/boot.d/${h##*/}"
    /bin/sh "$UD/.hooks/boot.d/${h##*/}" || true
done
sync
