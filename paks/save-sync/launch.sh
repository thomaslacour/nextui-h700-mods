#!/bin/sh
# Save Sync — sauvegardes PortMaster <-> Steam Cloud, écran natif minui-list.
#
# Le pak lit l'état (configuration, dernier rapport du moteur, dates des
# sauvegardes) et confie toute action au planificateur du pak
# ($BASE/scheduler/cronctl.sh), donc au moteur save-sync-engine :
#   - Synchroniser maintenant ............ cronctl.sh sync-now
#   - Garder la version Cloud / locale ... cronctl.sh sync-now --prefer … --game …
#     (seulement sur un jeu bloqué ; les deux versions sont sauvegardées avant)
#   - Synchro automatique ................ cronctl.sh start|stop (+ démarrage)
#   - Transferts automatiques ............ cronctl.sh live-on|live-off
# Chaque action demande une confirmation. Le pak ne lit jamais session.json.
PAK_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$PAK_DIR/lib/paplib.sh"
paplib_init
BASE="${SAVE_SYNC_BASE:-$USERDATA_PATH/save-sync}"
CTL="$BASE/scheduler/cronctl.sh"
CONF="$BASE/configuration/games-sync.conf"
REPORT="$BASE/reports/07-last-check.txt"
LIST="$TMP/save-sync-list.json"
GAMES="$TMP/save-sync-games.txt"
TEXT="$TMP/save-sync-text.txt"
trap 'rm -f "$LIST" "$GAMES" "$TEXT" "$TEXT.menu"; paplib_cleanup' EXIT INT TERM HUP

# --- lecture de l'état ---------------------------------------------------------
# Jeux configurés, un par ligne : clé|nom|appid|dossier|fichiers.
games() {
    [ -r "$CONF" ] || return 0
    awk -F'|' '
        { sub(/^[ \t]+/, ""); sub(/[ \t\r]+$/, "") }
        $0 == "" || /^#/ || NF < 7 { next }
        {
            name = $1
            if (name == "voidstranger") name = "Void Stranger"
            else name = toupper(substr(name, 1, 1)) substr(name, 2)
            print $1 "|" name "|" $2 "|" $3 "|" $6
        }' "$CONF"
}
game() { games | grep "^$1|" | head -n 1; }

# Ligne du rapport pour un jeu : « celeste: FAIL CONFLICT_BOTH_CHANGED » -> « FAIL CONFLICT_BOTH_CHANGED ».
result() { sed -n "s/^$1:[[:space:]]*//p" "$REPORT" 2>/dev/null | head -n 1; }

# État court affiché à droite : OK, FAIL, OFF ou ?.
short_state() {
    case "$(result "$1")" in
    OK*) echo OK ;;
    FAIL*) echo FAIL ;;
    SKIP*) echo OFF ;;
    *) echo '?' ;;
    esac
}

# Un jeu bloqué que l'utilisateur peut débloquer en choisissant un côté.
needs_choice() {
    case "$(result "$1")" in
    *CONFLICT_BOTH_CHANGED* | *INITIAL_MISMATCH_MANUAL_REVIEW* | *FIRST_UPLOAD_MANUAL_ONLY* | *REMOTE_DELETED_MANUAL_REVIEW*) return 0 ;;
    esac
    return 1
}

# « Time: 2026-10-19T15:00:00+0200 » -> « Oct 19 15:00 ».
last_check() {
    sed -n 's/^Time:[[:space:]]*//p' "$REPORT" 2>/dev/null | head -n 1 | awk -F'[-T:]' '
        NF >= 5 { split("Jan Feb Mar Apr May Jun Jul Aug Sep Oct Nov Dec", m, " "); printf "%s %d %s:%s\n", m[$2 + 0], $3, $4, $5 }' | grep . || echo never
}

aurelia_version() {
    bin="$BASE/bin/aurelia"
    [ -x "$bin" ] || { echo missing; return 0; }
    t=""; paplib_have timeout && t="timeout 4"
    v=$(AURELIA_CONFIG_DIR="$BASE/config" $t "$bin" --version 2>/dev/null | sed -n '1s/^[Aa]urelia[[:space:]]*//p')
    echo "${v:-unknown}"
}

# Instance crond du pak (la même détection que cronctl.sh : lecture de /proc).
cron_running() {
    for d in /proc/[0-9]*; do
        [ -r "$d/cmdline" ] || continue
        case "$(tr '\0' ' ' < "$d/cmdline" 2>/dev/null)" in *crond*"$BASE/scheduler/crontabs"*) return 0 ;; esac
    done
    return 1
}
live_enabled() { [ -f "$BASE/configuration/live-sync.enabled" ]; }

# Fréquences proposées : « libellé|expression cron » (configuration/schedule.txt).
SCHEDULE="$BASE/configuration/schedule.txt"
frequencies() {
    printf '%s\n' "15 min|*/15 * * * *" "30 min|*/30 * * * *" "1 hour|0 * * * *" \
        "2 hours|0 */2 * * *" "6 hours|0 */6 * * *" "Daily (noon)|0 12 * * *"
}
frequency_label() {
    expr=$(awk 'NF && $1 !~ /^#/ { print; exit }' "$SCHEDULE" 2>/dev/null | tr -d '\r')
    label=$(frequencies | awk -F'|' -v e="$expr" '$2 == e { print $1; exit }')
    echo "${label:-${expr:-?}}"
}

# Date de la sauvegarde locale la plus récente parmi les fichiers suivis.
save_date() { # <dossier> <fichiers séparés par des virgules>
    [ -d "$1" ] || { echo 'folder missing'; return 0; }
    newest=""
    for f in $(echo "$2" | tr ',' ' '); do
        m=$(stat -c %Y "$1/$f" 2>/dev/null || stat -f %m "$1/$f" 2>/dev/null) || continue
        [ -z "$newest" ] || [ "$m" -gt "$newest" ] && newest="$m"
    done
    [ -n "$newest" ] || { echo 'no local save'; return 0; }
    awk -v t="$newest" 'BEGIN { print strftime("%Y-%m-%d %H:%M", t) }' 2>/dev/null || date -r "$newest" '+%Y-%m-%d %H:%M'
}

# --- textes (log-viewer, police monospace) ---------------------------------------
game_detail() {
    IFS='|' read -r key name appid dir files <<EOF
$(game "$1")
EOF
    [ -n "$key" ] || { echo 'Unknown game'; return 0; }
    r=$(result "$key")
    printf '%s\n\n' "$name"
    printf '%-13s%s\n' 'Steam AppID' "$appid" 'Last save' "$(save_date "$dir" "$files")" \
        'Sync' "${r:-not checked yet}" 'Files' "$files" 'Folder' "$dir"
    if needs_choice "$key"; then
        printf '\nLocal and Cloud differ with no safe rule: choose\n'
        printf 'the version to keep (both are copied to\n'
        printf 'save-sync/backups/%s/ before anything is replaced).\n' "$key"
    fi
}

# Sous-menu d'un jeu : « libellé|action ».
game_menu() {
    echo "Game details|detail"
    if needs_choice "$1"; then
        echo "Keep Cloud version|cloud"
        echo "Keep local version|local"
    fi
}

build_logs() {
    ui_logs_start
    ui_log_add "Last sync" "$REPORT"
    ui_log_add "History" "$BASE/scheduler/events.log"
    ui_log_add "Cron output" "$BASE/scheduler/cron-job-output.log"
    ui_log_add "Pak log" "$LOGS_PATH/Save Sync.txt"
}

# --- écran principal ---------------------------------------------------------------
build_list() {
    ui_rows_start
    ui_row_info "Last sync" "$(last_check)"
    ui_row_action "Sync now" "sync"
    games > "$GAMES"
    if [ -s "$GAMES" ]; then
        ui_row_header "Games"
        while IFS='|' read -r key name _; do
            ui_row_info "$(ui_short "$name")" "$(short_state "$key")" "game:$key"
        done < "$GAMES"
    else
        ui_row_header "No games configured"
    fi
    ui_row_header "Settings"
    if cron_running; then ui_row_info "Auto sync" "on" "cron"; else ui_row_info "Auto sync" "off" "cron"; fi
    if live_enabled; then ui_row_info "Auto transfers" "on" "live"; else ui_row_info "Auto transfers" "off" "live"; fi
    ui_row_info "Sync every" "$(frequency_label)" "frequency"
    ui_row_info "Aurelia" "$(aurelia_version)"
    build_logs
    ui_row_action "Logs" "logs" "$(ui_logs_count)"
    ui_rows_finish "$LIST"
}

# --- actions -------------------------------------------------------------------------
# run_sync [--prefer cloud|local --game clé] : lance le moteur puis montre le rapport.
run_sync() {
    ui_run "Syncing with Steam Cloud…" /bin/sh "$CTL" sync-now "$@"
    ui_read "$REPORT" "Last sync"
}

game_screen() { # <clé>
    entry=$(game "$1")
    name=$(echo "$entry" | cut -d'|' -f2)
    game_menu "$1" > "$TEXT.menu"
    while choice=$(ui_pick "$TEXT.menu" "$name"); do
        case "$choice" in
        detail) game_detail "$1" > "$TEXT"; ui_read "$TEXT" "$name" ;;
        cloud | local)
            side="the Cloud version"; [ "$choice" = local ] && side="the local version"
            if ui_confirm "$name: keep $side?\n\nThe other version is copied to\nsave-sync/backups before it is replaced." "KEEP"; then
                run_sync --prefer "$choice" --game "$1"
                return 0
            fi ;;
        esac
    done
}

toggle_cron() {
    if cron_running; then
        ui_confirm "Turn off auto sync?\n\nIt will not restart at boot." "TURN OFF" || return 0
        /bin/sh "$CTL" stop; /bin/sh "$CTL" disable-boot
    else
        ui_confirm "Turn on auto sync?\n\nEvery $(frequency_label), and at boot." "TURN ON" || return 0
        /bin/sh "$CTL" start && /bin/sh "$CTL" enable-boot || ui_message "Could not start the scheduler.\nSee Logs." 4
    fi
}

choose_frequency() {
    frequencies > "$TEXT.menu"
    expr=$(ui_pick "$TEXT.menu" "Sync every" "CHOOSE" "$(cat "$SCHEDULE" 2>/dev/null)") || return 0
    printf '%s\n' "$expr" > "$SCHEDULE.tmp" && mv -f "$SCHEDULE.tmp" "$SCHEDULE" &&
        /bin/sh "$CTL" setup || ui_message "Could not save the schedule.\nSee Logs." 4
}

toggle_live() {
    if live_enabled; then
        ui_confirm "Turn off auto transfers?\n\nThe scheduler will only check." "TURN OFF" && /bin/sh "$CTL" live-off
    else
        ui_confirm "Allow the scheduler to transfer?\n\nSafe rules only: a conflict always\nwaits for your choice." "ALLOW" && /bin/sh "$CTL" live-on
    fi
}

# --- points d'entrée de test (stdout, aucune brique, aucun réseau) ------------------
case "${1:-}" in
--list-only) build_list; cat "$LIST"; exit 0 ;;
--detail) game_detail "$2"; exit 0 ;;
--menu-only) game_menu "$2"; exit 0 ;;
--logs-only) build_logs; cat "$UI_LOGS_FILE"; exit 0 ;;
--frequencies) frequencies; exit 0 ;;
esac

paplib_log "Save Sync"
paplib_have minui-list || { ui_message "Save Sync: minui-list is missing.\nRun mac/install-nextui.sh" 6; exit 1; }
[ -f "$CTL" ] || { ui_message "Save Sync: scheduler is missing.\nRun mac/deploy.sh" 6; exit 1; }

while :; do
    build_list
    ui_menu "$LIST" "Save Sync" || break # 2 = B, 3 = MENU
    case "$(ui_action_at)" in
    sync) ui_confirm "Sync now?\n\nWi-Fi required. A game in conflict\nstays blocked until you choose." "SYNC" && run_sync ;;
    game:*) game_screen "$(ui_action_at | cut -d: -f2)" ;;
    cron) toggle_cron ;;
    live) toggle_live ;;
    frequency) choose_frequency ;;
    logs) ui_logs "Logs" ;;
    esac
done
