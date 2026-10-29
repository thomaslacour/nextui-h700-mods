#!/bin/sh
# post-install.sh — run by `pak` after installing or updating Save Sync.pak
# (from the pak folder). Installs what lives outside the pak, the way
# mac/deploy.sh does:
#   .userdata/<platform>/save-sync/bin/save-sync-engine
#   .userdata/<platform>/save-sync/scheduler/{cronctl,check-updates}.sh
#   configuration/*: defaults only when missing (your settings are kept)
# then refreshes the isolated crontab; the daemon keeps its state (on or off).
# Never touches saves, the Steam session or Aurelia.
set -e
SD="${SDCARD_PATH:-/mnt/SDCARD}"
UD="$SD/.userdata/${PLATFORM:-h700}"
BASE="$UD/save-sync"

put() { # <file in payload/> <destination>: written as .new, then renamed
    mkdir -p "$(dirname "$2")"
    cp "payload/$1" "$2.new"
    chmod 755 "$2.new"
    mv "$2.new" "$2"
}
put save-sync-engine "$BASE/bin/save-sync-engine"
put scheduler/cronctl.sh "$BASE/scheduler/cronctl.sh"
put scheduler/check-updates.sh "$BASE/scheduler/check-updates.sh"
mkdir -p "$BASE/configuration"
for f in payload/defaults/*; do
    [ -s "$BASE/configuration/${f##*/}" ] || cp "$f" "$BASE/configuration/${f##*/}"
done

CTL="$BASE/scheduler/cronctl.sh"
export SAVE_SYNC_BASE="$BASE" SAVE_SYNC_AUTO_SH="$UD/auto.sh"
/bin/sh "$CTL" setup
if grep -q SAVE_SYNC_CRON_H700 "$UD/auto.sh" 2>/dev/null; then /bin/sh "$CTL" enable-boot; fi
/bin/sh "$CTL" tidy
sync
