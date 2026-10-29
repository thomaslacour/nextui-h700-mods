#!/bin/sh
# 12-pak-cli.sync.sh — commande `pak` disponible partout, Updater et Pak Store masqués.
#
# Le binaire vit dans Tools/h700/Updates.pak/pak (gestionnaire de paquets du
# Pak Store, paks/updates/cli). Ce hook :
#   - pose le lien /usr/bin/pak (dans le PATH d'adb, ssh et du Terminal) ; une
#     mise à jour de BaseOS réécrit / : le lien revient au démarrage suivant ;
#   - garde Updater et Pak Store dans nextui-mods/hidden-tools et masqués
#     (`pak hide`), en plus de la garde NextUI (10-nextui-mods) : une mise à
#     jour de NextUI qui les remet dans Tools les voit renommés en .pak.disabled.
# Idempotent, rapide (aucun réseau), sans effet si Updates.pak est absent.
SD="${SDCARD_PATH:-/mnt/SDCARD}"
PAK="$SD/Tools/${PLATFORM:-h700}/Updates.pak/pak"
[ -f "$PAK" ] || exit 0
[ "$(readlink /usr/bin/pak 2>/dev/null)" = "$PAK" ] || ln -sf "$PAK" /usr/bin/pak 2>/dev/null
"$PAK" hide Updater "Pak Store" >/dev/null 2>&1
exit 0
