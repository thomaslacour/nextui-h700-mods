#!/bin/sh
# paplib.sh — briques communes des paks NextUI H700 (Save Sync, Updates, Moonlight…).
#
# Même squelette que les paks communautaires (Tailscale, dufs) : environnement
# du pak, journal, menus natifs `minui-list`, messages `minui-presenter`, et le
# lecteur plein écran `log-viewer` pour les textes longs.
#
#   PAK_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
#   . "$PAK_DIR/lib/paplib.sh"
#   paplib_init
#   trap paplib_cleanup EXIT INT TERM HUP
#   ...                      # points d'entrée de test éventuels (stdout intact)
#   paplib_log "Mon Pak"     # puis tout part dans logs/Mon Pak.txt
#
# Sourcer ce fichier est sans effet : rien ne s'exécute avant un appel.

# --- Environnement -----------------------------------------------------------
paplib_init() {
    PLATFORM="${PLATFORM:-h700}"
    SDCARD_PATH="${SDCARD_PATH:-/mnt/SDCARD}"
    USERDATA_PATH="${USERDATA_PATH:-$SDCARD_PATH/.userdata/$PLATFORM}"
    LOGS_PATH="${LOGS_PATH:-$USERDATA_PATH/logs}"
    architecture=arm
    if uname -m 2>/dev/null | grep -q 64; then architecture=arm64; fi
    # Ordre des paks du magasin : le pak d'abord, puis le système.
    export PATH="$PAK_DIR/bin/$architecture:$PAK_DIR/bin/$PLATFORM:$PAK_DIR/bin:$SDCARD_PATH/.system/$PLATFORM/bin:$PATH"
    export LD_LIBRARY_PATH="$SDCARD_PATH/.system/$PLATFORM/lib:/usr/lib:/usr/lib/aarch64-linux-gnu${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
    mkdir -p "$USERDATA_PATH" "$LOGS_PATH" 2>/dev/null || true
}

# paplib_log <nom> : journal du pak (logs/<nom>.txt), remis à zéro à chaque
# lancement ; empêche aussi la mise en veille pendant que le pak est ouvert.
paplib_log() {
    rm -f "$LOGS_PATH/$1.txt" "$UI_STATE" "$UI_PICK_STATE" "$UI_PICK_STATE.in" 2>/dev/null || true
    exec >>"$LOGS_PATH/$1.txt" 2>&1
    echo "$0 $(date '+%Y-%m-%d %H:%M:%S')"
    printf '1\n' > /tmp/stay_awake 2>/dev/null || true
}

paplib_cleanup() {
    rm -f /tmp/stay_awake "$UI_ROWS_FILE" "$UI_IDS_FILE" "$UI_LOGS_FILE" \
        "$UI_PICK_LIST" "$UI_PICK_IDS" "$UI_PICK_STATE" "$UI_PICK_STATE.in" "$UI_STATE" 2>/dev/null || true
    killall -9 minui-presenter >/dev/null 2>&1 || true
}

paplib_have() { command -v "$1" >/dev/null 2>&1; }

# --- Messages (minui-presenter) ---------------------------------------------
# ui_message <texte> [secondes] : message bref (défaut 3 s).
ui_message() {
    paplib_have minui-presenter || { printf '%s\n' "$1" >&2; return 0; }
    killall -9 minui-presenter >/dev/null 2>&1 || true
    minui-presenter --message "$1" --timeout "${2:-3}" >/dev/null 2>&1
}

# ui_confirm <message> [texte-A] -> 0 = confirmé (A), 1 = annulé (B).
ui_confirm() {
    paplib_have minui-presenter || return 1
    minui-presenter --message "$1" --timeout 0 \
        --confirm-show --confirm-text "${2:-OK}" \
        --cancel-show --cancel-text "BACK" >/dev/null 2>&1
}

# ui_run <message> <commande…> : affiche « message » le temps de la commande
# (sortie dans le journal du pak) et renvoie son code retour.
# Le message est arrêté par son PID, en SIGKILL et sans « wait » : si la
# commande finit très vite, killall passerait avant que minui-presenter ait
# démarré ; et minui-presenter intercepte SIGTERM sans sortir tant qu'aucun
# événement n'arrive (--timeout -1) : un wait bloquerait le pak.
ui_run() {
    run_msg="$1"
    shift
    run_pid=""
    if paplib_have minui-presenter; then
        killall -9 minui-presenter >/dev/null 2>&1 || true
        minui-presenter --message "$run_msg" --timeout -1 >/dev/null 2>&1 &
        run_pid=$!
    fi
    "$@"
    run_rc=$?
    [ -z "$run_pid" ] || kill -9 "$run_pid" 2>/dev/null
    killall -9 minui-presenter >/dev/null 2>&1 || true
    return "$run_rc"
}

# --- Listes minui-list --------------------------------------------------------
# Pas de `jq` sur la console : le JSON est écrit à la main, un objet par ligne.
# Lignes alignées à gauche (défaut minui-list) : libellé à la marge, valeur au
# bord droit, comme les réglages MinUI.
#
#   ui_rows_start
#   ui_row_header "Jeux"                         # titre de section
#   ui_row_info   "Aurelia" "0.1.40"             # lecture, A sans effet
#   ui_row_info   "Celeste" "OK" "game:celeste"  # A = action
#   ui_row_action "Journaux" "logs" ["3"]        # action, valeur facultative
#   ui_rows_finish "$LIST"
#   ui_menu "$LIST" "Titre" || break             # 0 = A, 2 = B, 3 = MENU
#   action=$(ui_action_at)
TMP="${TMPDIR:-/tmp}"
UI_ROWS_FILE="$TMP/paplib-rows.json"
UI_IDS_FILE="$TMP/paplib-ids.txt"
UI_LOGS_FILE="$TMP/paplib-logs.txt"
UI_STATE="$TMP/paplib-state.json"
# Sous-listes (ui_pick) : fichiers distincts, le menu principal reste intact.
UI_PICK_LIST="$TMP/paplib-pick.json"
UI_PICK_IDS="$TMP/paplib-pick-ids.txt"
UI_PICK_STATE="$TMP/paplib-pick-state.json"
UI_TAB="$(printf '\t')" # séparateur libellé / texte de droite (ui_pick)

ui_rows_start() { : > "$UI_ROWS_FILE"; : > "$UI_IDS_FILE"; }

ui_json_escape() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' -e 's/\r//g'; }

# ui_short <texte> [max] : tronque avec « … » (défaut 26 caractères). La
# police des menus (~17 px par caractère) ferait passer un libellé plus long
# sous la valeur posée à droite.
ui_short() {
    printf '%s' "$1" | awk -v max="${2:-26}" '{
        if (length($0) > max) { s = substr($0, 1, max - 1); sub(/[ ]+$/, "", s); printf "%s…", s }
        else printf "%s", $0
    }'
}

# ui_row <libellé> <valeur|""> <action|""> : ligne générique. Une ligne sans
# action reste sélectionnable (le curseur ne la saute pas) mais A n'y fait
# rien : sans "show_confirm", minui-list masque la pastille et ignore A.
ui_row() {
    row="{\"name\": \"$(ui_json_escape "$1")\""
    [ -z "$2" ] || row="$row, \"options\": [\"$(ui_json_escape "$2")\"]"
    [ -z "$3" ] || row="$row, \"features\": {\"show_confirm\": true}"
    printf '%s}\n' "$row" >> "$UI_ROWS_FILE"
    printf '%s\n' "$3" >> "$UI_IDS_FILE"
}
ui_row_info() { ui_row "$1" "$2" "${3:-}"; }
ui_row_action() { ui_row "$1" "${3:-}" "$2"; }
ui_row_header() {
    printf '{"name": "%s", "features": {"is_header": true}}\n' "$(ui_json_escape "$1")" >> "$UI_ROWS_FILE"
    printf '\n' >> "$UI_IDS_FILE"
}

# ui_rows_finish <fichier> : tableau JSON attendu par --file. Le curseur
# revient sur la ligne validée au tour précédent ("selected" de premier
# niveau ; minui-list l'ignore s'il ne désigne pas une ligne sélectionnable).
ui_rows_finish() {
    {
        printf '{\n  "selected": %s,\n  "items": [\n' "$(ui_selected_index "$UI_STATE" | grep . || echo 0)"
        awk 'NR > 1 { printf ",\n" } { printf "    %s", $0 }' "$UI_ROWS_FILE"
        printf '\n  ]\n}\n'
    } > "$1"
}

# ui_menu <fichier.json> <titre> [état] [texte-A] [texte-B] -> code de minui-list.
# `--write-value state` est indispensable : sans lui, minui-list n'écrit que
# le libellé de la ligne validée, ui_action_at n'y trouve aucun index et A
# semble ne rien faire. Les messages [INFO] de la brique vont au journal.
ui_menu() {
    minui-list --file "$1" --format json --title "$2" --item-key items \
        --confirm-text "${4:-OPEN}" --cancel-text "${5:-QUIT}" \
        --write-location "${3:-$UI_STATE}" --write-value state --disable-auto-sleep >&2
}

# ui_selected_index <état> : index de la ligne validée. Le JSON indenté de
# minui-list a un "selected" de premier niveau (la ligne) et un "selected" par
# entrée (l'option) : on retient celui de profondeur 1.
ui_selected_index() {
    awk '{
        line = $0
        if (depth == 1 && match(line, /"selected"[ \t]*:[ \t]*[0-9]+/)) {
            s = substr(line, RSTART, RLENGTH); sub(/.*:[ \t]*/, "", s); print s + 0; exit
        }
        depth += gsub(/\{/, "{", line) - gsub(/\}/, "}", line)
    }' "$1" 2>/dev/null
}

# ui_action_at [état] : action de la ligne validée (vide = aucune).
ui_action_at() {
    n=$(ui_selected_index "${1:-$UI_STATE}")
    [ -n "$n" ] || return 0
    sed -n "$((n + 1))p" "$UI_IDS_FILE"
}

# ui_pick <fichier « libellé|valeur »> <titre> [texte-A] [valeur-courante] :
# sous-liste native. Écrit la valeur choisie ; renvoie 1 si la liste est vide
# ou si l'on ressort. Le curseur part de la valeur courante si elle est donnée,
# sinon il est conservé tant que la sous-liste ne change pas (même contenu).
# Libellé « gauche<TAB>droite » : texte aligné à droite (comme une valeur).
# Ligne « # texte » : en-tête gris, non sélectionnable.
ui_pick() {
    [ -s "${1:-}" ] || return 1
    pick_at=0
    if [ -n "${4:-}" ]; then
        pick_at=$(cut -d'|' -f2- "$1" | grep -nxF -- "$4" | head -1 | cut -d: -f1)
        pick_at=$(( ${pick_at:-1} - 1 ))
    elif cmp -s "$1" "$UI_PICK_STATE.in"; then pick_at=$(ui_selected_index "$UI_PICK_STATE" | grep . || echo 0); fi
    cp "$1" "$UI_PICK_STATE.in"
    : > "$UI_PICK_IDS"
    {
        printf '{\n  "selected": %s,\n  "items": [\n' "$pick_at"
        pick_first=1
        while IFS='|' read -r pick_label pick_value; do
            [ -n "$pick_label" ] || continue
            [ "$pick_first" -eq 1 ] || printf ',\n'
            pick_first=0
            case "$pick_label" in
            "# "*)
                printf '    {"name": "%s", "features": {"is_header": true}}' \
                    "$(ui_json_escape "$(ui_short "${pick_label#\# }" 32)")"
                printf '\n' >> "$UI_PICK_IDS"
                continue ;;
            esac
            pick_right=""
            case "$pick_label" in *"$UI_TAB"*) pick_right=${pick_label#*"$UI_TAB"}; pick_label=${pick_label%%"$UI_TAB"*} ;; esac
            printf '    {"name": "%s"' "$(ui_json_escape "$(ui_short "$pick_label" 32)")"
            [ -z "$pick_right" ] || printf ', "options": ["%s"]' "$(ui_json_escape "$pick_right")"
            printf ', "features": {"show_confirm": true}}'
            printf '%s\n' "$pick_value" >> "$UI_PICK_IDS"
        done < "$1"
        printf '\n  ]\n}\n'
    } > "$UI_PICK_LIST"
    grep -q . "$UI_PICK_IDS" || return 1
    minui-list --file "$UI_PICK_LIST" --format json --title "$2" --item-key items \
        --confirm-text "${3:-OPEN}" --cancel-text "BACK" \
        --write-location "$UI_PICK_STATE" --write-value state --disable-auto-sleep >&2 || return 1
    pick_n=$(ui_selected_index "$UI_PICK_STATE")
    [ -n "$pick_n" ] || return 1
    sed -n "$((pick_n + 1))p" "$UI_PICK_IDS"
}

# --- Textes longs (log-viewer) -------------------------------------------------
# ui_read <fichier> [titre] : lecture plein écran, petite police monospace,
# défilement libre (B revient). Sans le binaire : 12 premières lignes.
ui_read() {
    [ -r "$1" ] || { ui_message "Cannot read file:\n$1" 4; return 1; }
    if [ -x "$PAK_DIR/log-viewer" ]; then
        "$PAK_DIR/log-viewer" --file "$1" --title "${2:-}"
        return $?
    fi
    paplib_have minui-presenter || return 0
    minui-presenter --message "$(sed -n '1,12p' "$1")" --timeout 0 --confirm-show --confirm-text OK >/dev/null 2>&1
}

# Journaux proposés : ui_logs_start, ui_log_add <titre> <chemin> (ignoré si
# absent), puis ui_logs [titre] : sous-liste, A ouvre, B revient.
ui_logs_start() { : > "$UI_LOGS_FILE"; }
ui_log_add() { [ ! -r "${2:-}" ] || printf '%s|%s\n' "$1" "$2" >> "$UI_LOGS_FILE"; }
ui_logs_count() { if [ -s "$UI_LOGS_FILE" ]; then wc -l < "$UI_LOGS_FILE" | tr -d ' '; else echo 0; fi; }
ui_logs() {
    while logs_path=$(ui_pick "$UI_LOGS_FILE" "${1:-Logs}"); do
        ui_read "$logs_path" "$(awk -F'|' -v p="$logs_path" '$2 == p { print $1; exit }' "$UI_LOGS_FILE")"
    done
}
