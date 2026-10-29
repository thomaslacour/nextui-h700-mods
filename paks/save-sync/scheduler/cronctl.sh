#!/bin/sh
# Independently managed BusyBox cron instance for Save Sync.
# Existing system crontabs and existing NextUI auto.sh content are preserved.
set -u
export PATH=/sbin:/usr/sbin:/bin:/usr/bin:${PATH:-}
BASE="${SAVE_SYNC_BASE:-/mnt/SDCARD/.userdata/h700/save-sync}"
STATE="$BASE/scheduler"
CFG="$BASE/configuration"
SPOOL="$STATE/crontabs"
SCHED="$CFG/schedule.txt"
USCHED="$CFG/update-schedule.txt"
UJOB="$STATE/check-updates.sh"
CRONLOG="$STATE/crond.log"
PLATFORM="${PLATFORM:-h700}"
SDCARD="${SDCARD_PATH:-/mnt/SDCARD}"
UPD_DIR="${UPDATE_CHECK_DIR:-$SDCARD/.userdata/$PLATFORM/update-check}"
AUTO="${SAVE_SYNC_AUTO_SH:-/mnt/SDCARD/.userdata/h700/auto.sh}"
# Démarrage automatique : hook NextUI .hooks/boot.d (un fichier à nous, rien
# à éditer dans auto.sh). L'ancienne ligne d'auto.sh est migrée puis retirée.
BOOT_HOOK="${SAVE_SYNC_BOOT_HOOK:-$SDCARD/.userdata/$PLATFORM/.hooks/boot.d/50-save-sync.sh}"
ENGINE="$BASE/bin/save-sync-engine"
MARKER='# SAVE_SYNC_CRON_H700'
umask 077
mkdir -p "$SPOOL" "$CFG" "$BASE/reports" || exit 1
say() { printf '%s\n' "$*"; }
get_crond() {
    if command -v crond >/dev/null 2>&1; then command -v crond; return 0; fi
    if command -v busybox >/dev/null 2>&1 && busybox --list 2>/dev/null | grep -qx crond; then
        printf '%s\n' 'busybox'; return 0
    fi
    return 1
}
# Find only the dedicated cron instance; never kill unrelated cron daemons.
find_own_pid() {
    for proc in /proc/[0-9]*; do
        [ -f "$proc/cmdline" ] || continue
        cmd=$(tr '\000' ' ' < "$proc/cmdline" 2>/dev/null || true)
        case "$cmd" in
            *crond*"-c $SPOOL"*) printf '%s\n' "${proc##*/}"; return 0 ;;
        esac
    done
    return 1
}
cron_expr() {
    _file="${1:-$SCHED}"
    _default="${2:-}"
    if [ -f "$_file" ]; then
        # première ligne utile : on ignore commentaires et lignes vides
        line=$(awk 'NF && $1 !~ /^#/ {print; exit}' "$_file" 2>/dev/null | tr -d '\r')
    else
        line="$_default"
    fi
    if [ -z "$line" ]; then
        say "ERROR: schedule missing (${_file##*/})" >&2
        return 1
    fi
    if ! printf '%s\n' "$line" | awk '
        NF!=5 {exit 1}
        {for(i=1;i<=5;i++) if ($i !~ /^[0-9*,\/\-]+$/) exit 1}
    '; then
        say 'ERROR: invalid schedule; expected five cron fields, e.g. */30 * * * *'  >&2
        return 1
    fi
    printf '%s\n' "$line"
}
setup() {
    [ -x "$ENGINE" ] || { say "ERROR: missing executable $ENGINE"; return 1; }
    spec=$(cron_expr) || return 1
    # One entry in our isolated spool; no edits to /etc/crontabs or root crontab.
    printf '%s /bin/sh %s run >> %s 2>&1\n' "$spec" "$STATE/cronctl.sh" "$STATE/cron-job-output.log" > "$SPOOL/root.tmp" || return 1
    say "Cron expression: $spec"
    if [ -x "$UJOB" ]; then
        uspec=$(cron_expr "$USCHED" '17 */6 * * *') || return 1
        printf '%s /bin/sh %s >> %s 2>&1\n' "$uspec" "$UJOB" "$STATE/update-check-job-output.log" >> "$SPOOL/root.tmp" || return 1
        say "Update check expression: $uspec"
    fi
    mv "$SPOOL/root.tmp" "$SPOOL/root"
    chmod 600 "$SPOOL/root" 2>/dev/null || true
}
status() {
    say 'SAVE SYNC CRON STATUS'
    say "Schedule: $(cron_expr 2>/dev/null || echo MISSING)"
    if get_crond >/dev/null 2>&1; then say 'BusyBox crond: available'; else say 'BusyBox crond: NOT AVAILABLE'; fi
    if ownpid=$(find_own_pid); then say "Daemon: RUNNING (pid $ownpid)"; else say 'Daemon: STOPPED'; fi
    if [ -f "$BOOT_HOOK" ]; then say 'Start at boot: ENABLED (.hooks/boot.d)'
    elif [ -f "$AUTO" ] && grep -Fq "$MARKER" "$AUTO"; then say 'Start at boot: ENABLED (old auto.sh line)'
    else say 'Start at boot: DISABLED'; fi
    if [ -f "$CFG/games-sync.conf" ]; then say "Game configuration: $CFG/games-sync.conf"; else say 'Game configuration: MISSING'; fi
    if [ -f "$CFG/live-sync.enabled" ]; then
        say 'Transfer policy: LIVE SYNC - safe rules, manual conflicts'
    else
        say 'Transfer policy: CHECK ONLY - uploads/downloads BLOCKED'
    fi
    if [ -x "$UJOB" ]; then
        say 'Update checker: installed (detection only, never installs)'
    else
        say 'Update checker: not installed'
    fi
    if [ -r "$UPD_DIR/state" ]; then
        say 'Latest update check:'
        sed 's/^/  /' "$UPD_DIR/state"
    fi
    if [ -r "$STATE/events.log" ]; then
        say 'Latest events:'
        tail -12 "$STATE/events.log"
    fi
}
start() {
    setup || return 1
    if ownpid=$(find_own_pid); then say "Cron already running (pid $ownpid)"; return 0; fi
    EXE=$(get_crond) || { say 'ERROR: BusyBox crond not provided by this firmware'; return 1; }
    # An existing system crond may hold /var/run/crond.pid; fail explicitly.
    if [ "$EXE" = busybox ]; then
        busybox crond -b -l 8 -c "$SPOOL" -L "$CRONLOG" </dev/null >/dev/null 2>&1
    else
        "$EXE" -b -l 8 -c "$SPOOL" -L "$CRONLOG" </dev/null >/dev/null 2>&1
    fi
    rc=$?
    sleep 1
    if ownpid=$(find_own_pid); then
        say "Cron running (pid $ownpid)"; return 0
    fi
    say "ERROR: unable to start dedicated cron (exit $rc); inspect $CRONLOG"
    return 1
}
stop() {
    if pid=$(find_own_pid); then
        kill "$pid" || return 1
        say "Stopped Save Sync cron PID $pid"
    else say 'Save Sync cron already stopped'; fi
}
# Retire l'ancienne ligne d'auto.sh (versions précédentes), avec sauvegarde ;
# les autres lignes (Tailscale…) sont conservées.
remove_legacy_line() {
    [ -f "$AUTO" ] && grep -Fq "$MARKER" "$AUTO" || return 0
    cp -p "$AUTO" "$STATE/auto.sh.backup.$(date +%Y%m%d-%H%M%S)" || return 1
    grep -Fv "$MARKER" "$AUTO" > "$AUTO.tmp" || true
    cat "$AUTO.tmp" > "$AUTO"
    rm -f "$AUTO.tmp"
    say "Old boot line removed from $AUTO (other entries preserved)"
}
enable_boot() {
    mkdir -p "$(dirname "$BOOT_HOOK")" || return 1
    printf '#!/bin/sh\n# Save Sync scheduler (see save-sync/scheduler/cronctl.sh)\nexec /bin/sh "%s" start </dev/null >/dev/null 2>&1\n' \
        "$STATE/cronctl.sh" > "$BOOT_HOOK.tmp" && mv "$BOOT_HOOK.tmp" "$BOOT_HOOK" && chmod +x "$BOOT_HOOK" || return 1
    remove_legacy_line || return 1
    say "Boot hook installed: $BOOT_HOOK"
}
disable_boot() {
    rm -f "$BOOT_HOOK"
    remove_legacy_line || return 1
    say 'Boot hook removed'
}
# --- entretien (appelé à chaque passage du cron, et par « cronctl.sh tidy ») ---
# rotate <fichier> : au-delà de 128 Ko, l'ancien contenu passe en <fichier>.1
# (copie puis remise à zéro : crond peut garder le fichier ouvert).
rotate() {
    [ -f "$1" ] || return 0
    [ "$(wc -c < "$1" | tr -d ' ')" -gt "${SAVE_SYNC_LOG_MAX:-131072}" ] || return 0
    cp -f "$1" "$1.1" && : > "$1"
}
# keep_newest <n> <motif…> : supprime tout sauf les n plus récents.
keep_newest() {
    n="$1"; shift
    ls -1dt "$@" 2>/dev/null | sed -n "$((n + 1)),\$p" | while IFS= read -r old; do rm -rf -- "$old"; done
}
tidy() {
    for f in "$STATE/cron-job-output.log" "$STATE/update-check-job-output.log" "$CRONLOG" "$UPD_DIR/last-run.log"; do
        rotate "$f"
    done
    # Copies de sécurité du moteur : les 20 plus récentes par jeu.
    for g in "$BASE"/backups/*/; do
        [ -d "$g" ] && keep_newest 20 "$g"*/
    done
    # Anciennes versions d'Aurelia : la plus récente seulement.
    keep_newest 1 "$BASE"/bin/aurelia.previous-*
    # Dossiers de travail du moteur restés après une coupure (jamais pendant une synchro).
    [ -e "$STATE/live-sync.lock" ] || rm -rf "$BASE"/staging/* 2>/dev/null
    return 0
}

cmd="${1:-status}"
[ $# -eq 0 ] || shift
case "$cmd" in
    setup) setup ;;
    status) status ;;
    start) start ;;
    stop) stop ;;
    enable-boot) enable_boot ;;
    disable-boot) disable_boot ;;
    # Transferts automatiques du cron (opt-in). Les commandes manuelles
    # ci-dessous transfèrent toujours : elles sont lancées à la demande.
    live-on) : > "$CFG/live-sync.enabled" && say 'Transfer policy: LIVE SYNC' ;;
    live-off) rm -f "$CFG/live-sync.enabled" && say 'Transfer policy: CHECK ONLY' ;;
    # run : tâche du cron ; ne transfère que si l'opt-in Live est présent.
    run) tidy
        if [ -f "$CFG/live-sync.enabled" ]; then exec "$ENGINE" --apply; else exec "$ENGINE" --check; fi ;;
    tidy) tidy ;;
    check-now) exec "$ENGINE" --check ;;
    # sync-now [--prefer cloud|local --game NOM] : synchro immédiate ; avec
    # --prefer, le côté choisi gagne pour ce jeu (copies de sauvegarde avant).
    sync-now) exec "$ENGINE" --apply "$@" ;;
    updates-now) exec "$UJOB" ;;
    *) say 'Usage: cronctl.sh {status|setup|start|stop|enable-boot|disable-boot|live-on|live-off|check-now|sync-now [--prefer cloud|local --game NOM]|updates-now}'; exit 2 ;;
esac
