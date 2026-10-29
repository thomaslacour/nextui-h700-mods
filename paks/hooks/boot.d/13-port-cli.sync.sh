#!/bin/sh
# 13-port-cli.sync.sh — commande `port` (ports PortMaster) disponible partout.
#
# Le binaire vit dans .userdata/h700/ports/port (même binaire que `pak`, le nom
# choisit le mode : paks/updates/cli) avec son pont vers le moteur PortMaster
# (port-hm.py). Ce hook pose le lien /usr/bin/port (PATH d'adb, ssh et du
# Terminal) ; une mise à jour de BaseOS réécrit / : le lien revient au
# démarrage suivant. Idempotent, rapide, sans effet si le binaire est absent.
PORT="${USERDATA_PATH:-${SDCARD_PATH:-/mnt/SDCARD}/.userdata/${PLATFORM:-h700}}/ports/port"
[ -f "$PORT" ] || exit 0
[ "$(readlink /usr/bin/port 2>/dev/null)" = "$PORT" ] || ln -sf "$PORT" /usr/bin/port 2>/dev/null
exit 0
