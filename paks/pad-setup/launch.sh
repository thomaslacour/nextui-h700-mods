#!/bin/sh
# Pad Setup — set up an external pad that the SDL database maps wrongly.
#
# The SDL pad database (.userdata/h700/gamecontrollerdb.txt) is sometimes
# wrong: the OhSnap MCON says it is an Xbox Series pad but also declares
# KEY_RECORD (167); NextUI's SDL numbers buttons by ascending key code, so
# every button is shifted by one and its A acts as B.
#
# The pak asks for each control in turn (A, B, X, Y, L1, R1, L2, R2, SELECT,
# START, Home, d-pad, sticks) and reads the press with pad-probe, which goes
# through the console's own SDL: the bN / aN / hN.M indices and the GUID are
# exactly what the launcher, minarch, Moonlight and the ports see.
# Nothing pressed for 6 s, or the console's A: control skipped; the
# console's B: cancel.
#
# The resulting line ("GUID,Name,a:b1,...,platform:Linux,") is appended to
# pads-overrides.txt (ports) and gamecontrollerdb.txt (SDL keeps the last line
# for a GUID), and recorded in pads-setup.txt, which mac/deploy.sh and
# mac/install-nextui.sh put back after the repo's overrides: reinstalling from
# the Mac does not lose it. The three files are backed up before each write
# (pads-backup/<date>/, 5 kept); "Undo last change" restores the newest.
#
# Test entry points (no screen):
#   launch.sh --list-only          menu as JSON
#   launch.sh --pads               pads seen by SDL
#   launch.sh --capture <guid>     capture, print the line (no minui-presenter)
#   launch.sh --save <line>        save a line
#   launch.sh --undo               restore the newest backup
PAK_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$PAK_DIR/lib/paplib.sh"
paplib_init
PROBE="$PAK_DIR/pad-probe"
OVR="$USERDATA_PATH/pads-overrides.txt"
DB="$USERDATA_PATH/gamecontrollerdb.txt"
SETUP="$USERDATA_PATH/pads-setup.txt"
BACKUPS="$USERDATA_PATH/pads-backup"
WAIT="${PADSETUP_WAIT:-6}" # seconds before a control is skipped
LIST="$TMP/pad-setup-list.json"
PADS="$TMP/pad-setup-pads.txt"
trap 'rm -f "$LIST" "$PADS"; paplib_cleanup' EXIT INT TERM HUP

# Controls asked for: SDL name|text shown|kind (for pad-probe).
controls() {
    cat <<'EOF'
a|Press A|button
b|Press B|button
x|Press X|button
y|Press Y|button
leftshoulder|Press L1|button
rightshoulder|Press R1|button
lefttrigger|Press L2|trigger
righttrigger|Press R2|trigger
back|Press SELECT|button
start|Press START|button
guide|Press Home|button
dpup|Press D-pad UP|dpad
dpdown|Press D-pad DOWN|dpad
dpleft|Press D-pad LEFT|dpad
dpright|Press D-pad RIGHT|dpad
leftx|Push left stick RIGHT|stick
lefty|Push left stick DOWN|stick
leftstick|Click left stick (L3)|button
rightx|Push right stick RIGHT|stick
righty|Push right stick DOWN|stick
rightstick|Click right stick (R3)|button
EOF
}

# --- pads -----------------------------------------------------------------------
# pads: "guid<TAB>name<TAB>known" per external pad, into $PADS.
pads() {
    "$PROBE" list "$DB" | cut -f2- > "$PADS"
    echo "pads: $(wc -l < "$PADS" | tr -d ' ') external" >&2
}
pad_name() { awk -F'\t' -v g="$1" '$1 == g { print $2; exit }' "$PADS"; }
has_guid() { [ -f "$2" ] && grep -q "^$1," "$2"; }
pad_state() { # <guid> <known>
    if has_guid "$1" "$SETUP"; then echo custom
    elif has_guid "$1" "$OVR"; then echo fixed
    elif [ "$2" = 1 ]; then echo known
    else echo unknown; fi
}

# --- capture ----------------------------------------------------------------------
# prompt <text>: shown while pad-probe waits (minui-presenter with no timeout
# ignores the buttons; stopped with SIGKILL, see ui_run).
PROMPT_PID=""
prompt() {
    prompt_stop
    [ -z "${PADSETUP_NO_UI:-}" ] && paplib_have minui-presenter || return 0
    minui-presenter --message "$1" --timeout -1 >/dev/null 2>&1 &
    PROMPT_PID=$!
}
prompt_stop() {
    [ -z "$PROMPT_PID" ] || kill -9 "$PROMPT_PID" 2>/dev/null
    PROMPT_PID=""
    killall -9 minui-presenter >/dev/null 2>&1 || true
}

# capture <guid> <name>: puts the line in $LINE, the counts in SET / SKIPPED.
# Returns 1 = cancelled (console B), 2 = pad disconnected, 3 = nothing pressed.
capture() {
    LINE="" SET=0 SKIPPED=0
    cap_used="" cap_binds="" cap_n=0
    cap_total=$(controls | wc -l | tr -d ' ')
    # (fd 3: pad-probe and minui-presenter must not read the list)
    while IFS='|' read -r cap_id cap_text cap_kind <&3; do
        cap_n=$((cap_n + 1))
        prompt "$2\n\n$cap_text\n\n$cap_n / $cap_total · wait $WAIT s to skip\nConsole A: skip · B: cancel"
        cap_out=$("$PROBE" wait "$1" "$WAIT" "$cap_kind" $cap_used 2>/dev/null)
        echo "capture: $cap_id -> ${cap_out:-?}"
        case "$cap_out" in
        cancel) prompt_stop; return 1 ;;
        gone | "") prompt_stop; return 2 ;;
        skip) SKIPPED=$((SKIPPED + 1)); continue ;;
        esac
        cap_raw=${cap_out#* }
        cap_used="$cap_used $cap_raw"
        # A stick takes the whole axis: its other direction is used too.
        case "$cap_raw" in a*+) cap_used="$cap_used ${cap_raw%+}-" ;; a*-) cap_used="$cap_used ${cap_raw%-}+" ;; esac
        cap_binds="$cap_binds$cap_id:${cap_out%% *},"
        SET=$((SET + 1))
    done 3<<EOF
$(controls)
EOF
    prompt_stop
    [ "$SET" -gt 0 ] || return 3
    LINE="$1,$(printf '%s' "$2" | tr -d ','),${cap_binds}platform:Linux,"
}

# --- saving ---------------------------------------------------------------------
backup() {
    bk_dir="$BACKUPS/$(date '+%Y%m%d-%H%M%S')"
    mkdir -p "$bk_dir" || return 1
    for bk_f in "$OVR" "$DB" "$SETUP"; do
        if [ -f "$bk_f" ]; then cp -p "$bk_f" "$bk_dir/" || return 1
        else : > "$bk_dir/$(basename "$bk_f").absent"; fi
    done
    # Keep the 5 newest.
    ls -1d "$BACKUPS"/*/ 2>/dev/null | sort -r | tail -n +6 | while read -r bk_old; do rm -rf "$bk_old"; done
    echo "backup: $bk_dir"
}

# save_line <line>: backs up, removes this pak's previous line for the GUID,
# then appends the new one to the three files.
save_line() {
    sv_guid=${1%%,*}
    sv_old="$TMP/pad-setup-old.txt"
    backup || return 1
    # Previous block for this GUID in pads-setup.txt: comment + line.
    awk -v g="$sv_guid," 'index($0, g) == 1 { if (c != "") print c; print } { c = /^#/ ? $0 : "" }' \
        "$SETUP" 2>/dev/null > "$sv_old"
    sv_note="# Pad Setup: $(printf '%s' "$1" | cut -d, -f2) ($(date '+%Y-%m-%d %H:%M'))"
    for sv_f in "$OVR" "$DB" "$SETUP"; do
        if [ -s "$sv_old" ] && [ -f "$sv_f" ]; then
            grep -vxF -f "$sv_old" "$sv_f" > "$sv_f.new" || [ $? -eq 1 ] || return 1
        elif [ -f "$sv_f" ]; then cp "$sv_f" "$sv_f.new" || return 1
        elif [ "$sv_f" = "$SETUP" ]; then
            printf '# Pad mappings made on the console with Settings > Controller > Pad Setup.\n' > "$sv_f.new"
        else : > "$sv_f.new"; fi
        # A file without a final newline: ours must not stick to its last line.
        [ ! -s "$sv_f.new" ] || [ -z "$(tail -c 1 "$sv_f.new")" ] || echo >> "$sv_f.new"
        printf '%s\n%s\n' "$sv_note" "$1" >> "$sv_f.new" && mv -f "$sv_f.new" "$sv_f" || return 1
    done
    rm -f "$sv_old"
    sync
    echo "saved: $1"
}

last_backup() { ls -1d "$BACKUPS"/*/ 2>/dev/null | sort | tail -n 1; }
backup_label() { # 20261022-190512 -> 22/10 19:05
    basename "$1" | sed 's/^....\(..\)\(..\)-\(..\)\(..\).*/\2\/\1 \3:\4/'
}
undo() {
    un_dir=$(last_backup)
    [ -n "$un_dir" ] || return 1
    for un_f in "$OVR" "$DB" "$SETUP"; do
        un_b="$un_dir/$(basename "$un_f")"
        if [ -f "$un_b" ]; then cp -p "$un_b" "$un_f.new" && mv -f "$un_f.new" "$un_f" || return 1
        elif [ -f "$un_b.absent" ]; then rm -f "$un_f"; fi
    done
    rm -rf "$un_dir"
    sync
    echo "restored: $un_dir"
}

# --- screen -------------------------------------------------------------------------
build_list() {
    pads
    ui_rows_start
    ui_row_header "Connected pads"
    if [ -s "$PADS" ]; then
        while IFS="$UI_TAB" read -r bl_guid bl_name bl_known; do
            ui_row_info "$(ui_short "$bl_name" 22)" "$(pad_state "$bl_guid" "$bl_known")" "pad:$bl_guid"
        done < "$PADS"
    else
        ui_row_info "No pad connected" ""
        ui_row_action "Check again" "refresh"
    fi
    bl_last=$(last_backup)
    if [ -n "$bl_last" ]; then
        ui_row_header "Backup"
        ui_row_action "Undo last change" "undo" "$(backup_label "$bl_last")"
    fi
    ui_rows_finish "$LIST"
}

set_up() { # <guid>
    su_name=$(pad_name "$1")
    [ -n "$su_name" ] || { ui_message "The pad is no longer connected." 3; return 0; }
    ui_confirm "Set up $su_name?\n\nPress each control when asked,\non the pad itself." "START" || return 0
    capture "$1" "$su_name"
    case $? in
    1) ui_message "Cancelled: nothing saved." 3; return 0 ;;
    2) ui_message "The pad disconnected: nothing saved." 4; return 0 ;;
    3) ui_message "Nothing pressed: nothing saved." 3; return 0 ;;
    esac
    su_skip=""
    [ "$SKIPPED" -eq 0 ] || su_skip=", $SKIPPED skipped"
    ui_confirm "$SET controls set$su_skip.\n\nSave this mapping?" "SAVE" || return 0
    if ! save_line "$LINE"; then
        ui_message "Cannot write to the SD card:\nnothing changed." 4
        return 0
    fi
    if minui-presenter --message "Saved.\n\nRelaunch your game to use it,\nor restart the console." --timeout 0 \
        --confirm-show --confirm-text RESTART --cancel-show --cancel-text LATER >/dev/null 2>&1; then
        touch /tmp/reboot
        exit 0
    fi
}

case "${1:-}" in
--list-only) build_list 2>/dev/null; cat "$LIST"; exit 0 ;;
--pads) "$PROBE" list "$DB"; exit $? ;;
--capture)
    pads
    PADSETUP_NO_UI=1
    capture "$2" "$(pad_name "$2")" || { echo "capture failed ($?)"; exit 1; }
    echo "set=$SET skipped=$SKIPPED"
    echo "$LINE"
    exit 0 ;;
--save) save_line "$2"; exit $? ;;
--undo) undo; exit $? ;;
esac

paplib_log "Pad Setup"
paplib_have minui-list || { ui_message "Pad Setup: minui-list is missing." 4; exit 1; }
while :; do
    build_list
    ui_menu "$LIST" "Pad Setup" "" "SET UP" || break
    case "$(ui_action_at)" in
    pad:*) set_up "$(ui_action_at | cut -d: -f2-)" ;;
    undo)
        ui_confirm "Undo the change of $(backup_label "$(last_backup)")?\n\nThe pad files go back\nto how they were before." "UNDO" || continue
        if undo; then ui_message "Done: previous pad files restored." 3
        else ui_message "Cannot restore the backup." 4; fi ;;
    esac
done
