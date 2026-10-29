#!/bin/sh
# Updates — mises à jour de BaseOS et de NextUI, au même endroit.
#
# BaseOS (pvaibhav/BaseOS) : installé ici, prudemment (inspiré de Base Jumper,
# SundownerSport, MIT, en plus strict) :
#   - seulement la release « latest » (jamais une pré-version), strictement plus
#     récente que l'installée, et le fichier exact baseos-<modèle>-<version>.bosupd ;
#   - chargeur branché obligatoire, réseau présent, place libre suffisante, aucune
#     synchro Save Sync en cours ;
#   - téléchargement dans un fichier caché, taille exacte, SHA-256 OBLIGATOIRE
#     (SHA256SUMS de la release) et, s'il est publié, identique à l'empreinte que
#     GitHub annonce pour le fichier — sinon rien n'est posé ;
#   - le .bosupd n'est déposé à la racine de la carte qu'après tout cela, puis
#     confirmation avant de redémarrer (BaseOS l'applique au démarrage et l'efface).
#
# NextUI (LoveRetro/NextUI) : version installée et disponible, notes, et l'état
# de nos modifications (nextui-mods, voir nextui/README.md) : la réserve est-elle
# prête pour la nouvelle version ? « Install » lance l'Updater officiel
# (LoveRetro/nextui-updater-pak), masqué des Tools (Updater.pak.disabled, liste
# hidden-tools de la garde) : ce pak est la seule entrée des mises à jour.
# Chargeur et absence de synchro exigés, avertissement si non préparée.
# Préparation sans le Mac : « Prepare <version> » (et « Install » s'il le faut)
# télécharge la réserve compilée par GitHub Actions (nextui-mods/fetch.sh,
# vérifiée par SHA-256) ; le vérificateur de mises à jour le fait déjà seul.
# « NextUI Updater » ouvre l'Updater pour ses autres fonctions (mise à jour
# complète, autre version) : il n'est plus accessible ailleurs.
#
# Paks (catalogue du Pak Store, LoveRetro/nextui-pak-store) : les paks installés
# qui ont une mise à jour, mise à jour une par une ou toutes, par la commande
# `pak` (paks/updates/cli, binaire Updates.pak/pak, aussi /usr/bin/pak) : archive
# vérifiée (SHA-256 de la release GitHub), fichiers de réglages gardés. Le Pak
# Store est masqué comme l'Updater (hidden-tools) ; Updates le re-masque à
# chaque ouverture si une mise à jour de NextUI l'a remis.
PAK_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$PAK_DIR/lib/paplib.sh"
paplib_init
RELEASE_FILE="${BASEOS_RELEASE_FILE:-/etc/baseos-release}"
VERSION_FILE="${NEXTUI_VERSION_FILE:-$SDCARD_PATH/.system/version.txt}"
ROUTE_FILE="${ROUTE_FILE:-/proc/net/route}"
POWER_DIR="${POWER_SUPPLY_DIR:-/sys/class/power_supply}"
MODS="$USERDATA_PATH/nextui-mods"
FETCH="$MODS/fetch.sh"
LOCK="$USERDATA_PATH/save-sync/scheduler/live-sync.lock"
# L'Updater, visible ou masqué par la garde (Updater.pak.disabled).
UPDATER=""
for d in "$SDCARD_PATH/Tools/$PLATFORM/Updater.pak" "$SDCARD_PATH/Tools/$PLATFORM/Updater.pak.disabled"; do
    [ -f "$d/launch.sh" ] && { UPDATER="$d/launch.sh"; break; }
done
PAK="${PAK_BIN:-$PAK_DIR/pak}"
WORK="$TMP/updates"
LIST="$TMP/updates-list.json"
PART="$SDCARD_PATH/.baseos-update.part"
trap 'rm -rf "$WORK" "$LIST" "$PART"; paplib_cleanup' EXIT INT TERM HUP
mkdir -p "$WORK"

field() { sed -n "s/^$1=//p" "$RELEASE_FILE" 2>/dev/null | tr -d '"' | head -n 1; }
B_INSTALLED=$(field BASEOS_VERSION)
MODEL=$(field BASEOS_TARGET)
N_INSTALLED=$(sed -n 1p "$VERSION_FILE" 2>/dev/null | tr -d '\r ')
CURL="$SDCARD_PATH/.system/$PLATFORM/bin/curl"
[ -x "$CURL" ] || CURL=curl

vnum() { case "$1" in "" | *[!0-9.]*) return 1 ;; esac; echo "$1" | awk -F. '{ printf "%d%03d%03d", $1, $2, $3 }'; }
newer() { a=$(vnum "$1") && b=$(vnum "$2") && [ "$a" -gt "$b" ]; } # <candidate> <installée>

# --- conditions ----------------------------------------------------------------------
online() { awk '$2 == "00000000" { f = 1 } END { exit !f }' "$ROUTE_FILE" 2>/dev/null; }
on_power() {
    for d in "$POWER_DIR"/*; do
        case "$(cat "$d/type" 2>/dev/null)" in USB | Mains) [ "$(cat "$d/online" 2>/dev/null)" = 1 ] && return 0 ;; esac
    done
    return 1
}
free_mb() { df -m "$SDCARD_PATH" 2>/dev/null | awk 'END { print $4 }'; }
sync_running() { [ -e "$LOCK" ]; }

# --- releases GitHub ------------------------------------------------------------------
# release <dépôt> <préfixe> : dernière release stable dans $WORK/<préfixe>.{json,assets,notes}
# (fichiers : « nom taille url empreinte ») ; affiche le tag sans « v ».
release() {
    "$CURL" -sf --connect-timeout 10 -m 30 -o "$WORK/$2.json" "https://api.github.com/repos/$1/releases/latest" || return 1
    awk -F'"' '
        $2 == "assets" { a = 1 } !a { next }
        $2 == "name" { n = $4; s = ""; d = "" }
        $2 == "size" { s = $3; gsub(/[^0-9]/, "", s) }
        $2 == "digest" { d = $4; sub(/^sha256:/, "", d) }
        $2 == "browser_download_url" { print n, s, $4, (d == "" ? "-" : d) }
    ' "$WORK/$2.json" > "$WORK/$2.assets"
    sed -n 's/^ *"body": *"\(.*\)",\{0,1\}$/\1/p' "$WORK/$2.json" | head -n 1 |
        sed -e 's/\\r//g' -e 's/\\n/\n/g' -e 's/\\"/"/g' -e 's/\\\\/\\/g' > "$WORK/$2.notes"
    sed -n 's/^ *"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' "$WORK/$2.json" | head -n 1 | grep .
}

# check : renseigne B_LATEST, ASSET, SIZE, URL, DIGEST, SUMS (BaseOS) et
# N_LATEST, N_BUILD (identifiant de build NextUI, = ligne 1 de version.txt).
check() {
    B_LATEST="" ASSET="" SIZE="" URL="" DIGEST="" SUMS="" N_LATEST="" N_BUILD="" P_ERR="no network"
    online || { ERR="No network: turn on Wi-Fi."; return 1; }
    paks_check
    B_LATEST=$(release "${BASEOS_REPO:-pvaibhav/BaseOS}" baseos) ||
        { ERR="GitHub is not answering (Wi-Fi, or wrong date and time)."; return 1; }
    set -- $(awk -v n="baseos-$MODEL-$B_LATEST.bosupd" '$1 == n' "$WORK/baseos.assets")
    ASSET="${1:-}" SIZE="${2:-}" URL="${3:-}" DIGEST="${4:-}"
    SUMS=$(awk '$1 == "SHA256SUMS" { print $3 }' "$WORK/baseos.assets")
    N_LATEST=$(release "${NEXTUI_REPO:-LoveRetro/NextUI}" nextui) || N_LATEST=""
    N_BUILD=$(sed -n 's/^\(NextUI-[0-9]*-[0-9]*\)-.*/\1/p' "$WORK/nextui.assets" 2>/dev/null | head -n 1)
    return 0
}

# --- paks du catalogue (commande pak) ---------------------------------------------------
# $WORK/paks.tsv : id, nom, nom du magasin, installée, disponible, état, masqué
# (pak list --porcelain) ; P_ERR si le catalogue n'a pas pu être lu.
paks_check() {
    P_ERR=""
    : > "$WORK/paks.tsv"
    [ -x "$PAK" ] && [ -f "$PAK" ] || { P_ERR="pak command missing"; return 0; }
    "$PAK" update > "$WORK/pak-update.txt" 2>&1 || P_ERR="catalogue unreachable"
    "$PAK" list --porcelain > "$WORK/paks.tsv" 2>/dev/null || P_ERR="${P_ERR:-cannot list paks}"
}
paks_count() { awk -F'\t' -v s="$1" '$6 == s' "$WORK/paks.tsv" 2>/dev/null | wc -l | tr -d ' '; }

# --- NextUI ------------------------------------------------------------------------------
nextui_newer() { [ -n "$N_BUILD" ] && [ "$N_BUILD" != "$N_INSTALLED" ]; }
# Version lisible : le tag si l'identifiant installé est celui de la dernière release.
nextui_installed_label() { if [ -n "$N_BUILD" ] && [ "$N_BUILD" = "$N_INSTALLED" ]; then echo "$N_LATEST"; else echo "${N_INSTALLED#NextUI-}"; fi; }
# État de nos modifications pour la version qui compte (la nouvelle s'il y en a une).
mods_state() {
    if nextui_newer; then
        if [ -d "$MODS/$N_BUILD" ]; then echo "ready for $N_LATEST"; else echo "NOT PREPARED"; fi
    else
        st=$(sed -n 's/^status=//p' "$MODS/state" 2>/dev/null)
        case "$st" in active | reapplied) echo active ;; "") echo "not installed" ;; *) echo "$st" ;; esac
    fi
}

# Préparation en ligne possible ? (fetch.sh et dépôt posés par mac/install-nextui.sh)
can_fetch() { [ -f "$FETCH" ] && [ -s "$MODS/repo" ]; }
# prepare : réserve de la nouvelle version téléchargée et vérifiée (ERR sinon).
prepare() {
    out=$(NEXTUI_MODS_DIR="$MODS" sh "$FETCH" "$N_BUILD" "v$N_LATEST" 2>&1)
    rc=$?
    echo "$out"
    [ $rc -eq 0 ] && return 0
    ERR=$(printf '%s\n' "$out" | sed -n 's/^error: //p' | tail -n 1)
    [ $rc -eq 3 ] && ERR="Not built online yet for $N_LATEST:\ntry again later, or prepare it on the Mac."
    ERR="${ERR:-Preparation failed.}"
    return 1
}

# --- BaseOS : téléchargement vérifié -------------------------------------------------------
download() {
    [ -n "$ASSET" ] || { ERR="BaseOS $B_LATEST has no file for this model ($MODEL)."; return 1; }
    newer "$B_LATEST" "$B_INSTALLED" || { ERR="BaseOS $B_INSTALLED is already up to date."; return 1; }
    on_power || { ERR="Plug in the charger: the update must run on mains power."; return 1; }
    ! sync_running || { ERR="A Save Sync sync is running: try again in a minute."; return 1; }
    need=$((SIZE / 1048576 + 50))
    [ "$(free_mb)" -ge "$need" ] 2>/dev/null || { ERR="Not enough space: $need MB needed."; return 1; }
    [ -n "$SUMS" ] && "$CURL" -sfL --connect-timeout 10 -m 30 -o "$WORK/SHA256SUMS" "$SUMS" ||
        { ERR="Checksums (SHA256SUMS) not found: update refused."; return 1; }
    sum=$(awk -v n="$ASSET" '$2 == n || $2 == "*" n { print $1 }' "$WORK/SHA256SUMS" | head -n 1)
    [ ${#sum} -eq 64 ] || { ERR="$ASSET is not in the checksums: update refused."; return 1; }
    [ "$DIGEST" = "-" ] || [ "$DIGEST" = "$sum" ] || { ERR="Checksums disagree: update refused."; return 1; }
    rm -f "$PART"
    "$CURL" -sfL --connect-timeout 10 -m 1800 -o "$PART" "$URL" || { ERR="Download interrupted."; return 1; }
    [ "$(wc -c < "$PART" | tr -d ' ')" = "$SIZE" ] || { ERR="Incomplete file: deleted."; return 1; }
    [ "$(sha256sum "$PART" | cut -d' ' -f1)" = "$sum" ] || { ERR="Damaged file (wrong checksum): deleted."; return 1; }
    rm -f "$SDCARD_PATH"/baseos-*.bosupd
    mv -f "$PART" "$SDCARD_PATH/$ASSET" && sync || { ERR="Cannot write to the SD card."; return 1; }
    echo "$ASSET verified ($sum)"
}

# --- écran ------------------------------------------------------------------------------------
available() { # <dernière> <plus récente ?>
    if [ -z "$1" ]; then echo "${ERR:+error}${ERR:-?}"; elif [ "$2" = yes ]; then echo "$1"; else echo "up to date"; fi
}
build_list() {
    ui_rows_start
    ui_row_header "BaseOS"
    ui_row_info "Installed" "${B_INSTALLED:-?}"
    b_new=no; [ -n "$B_LATEST" ] && newer "$B_LATEST" "$B_INSTALLED" && b_new=yes
    ui_row_info "Available" "$(available "$B_LATEST" $b_new)"
    [ -s "$WORK/baseos.notes" ] && ui_row_action "Release notes" "notes:baseos" "$B_LATEST"
    [ $b_new = yes ] && ui_row_action "Install $B_LATEST" "install"
    ui_row_header "NextUI"
    ui_row_info "Installed" "$(nextui_installed_label)"
    n_new=no; nextui_newer && n_new=yes
    ui_row_info "Available" "$(available "$N_LATEST" $n_new)"
    ui_row_info "Modifications" "$(mods_state)"
    [ $n_new = yes ] && [ ! -d "$MODS/$N_BUILD" ] && can_fetch && ui_row_action "Prepare $N_LATEST" "prepare" "download"
    [ -s "$WORK/nextui.notes" ] && ui_row_action "Release notes" "notes:nextui" "$N_LATEST"
    [ $n_new = yes ] && [ -n "$UPDATER" ] && ui_row_action "Install $N_LATEST" "updater"
    [ -n "$UPDATER" ] && ui_row_action "NextUI Updater" "updater-open" "other options"
    ui_row_header "Paks"
    n_up=$(paks_count update)
    if [ -n "$P_ERR" ] && [ "$n_up" = 0 ]; then
        ui_row_info "Catalogue" "$P_ERR"
    elif [ "$n_up" = 0 ]; then
        ui_row_info "Installed paks" "up to date"
    else
        [ "$n_up" -gt 1 ] && ui_row_action "Update all paks" "pak:all" "$n_up"
        while IFS="$(printf '\t')" read -r p_id p_name p_title p_have p_new p_state p_hidden; do
            [ "$p_state" = update ] && ui_row_action "$(ui_short "$p_title" 18)" "pak:$p_id" "$p_have → $p_new"
        done < "$WORK/paks.tsv"
    fi
    while IFS="$(printf '\t')" read -r p_id p_name p_title p_have p_new p_state p_hidden; do
        [ "$p_state" = held ] && ui_row_info "$(ui_short "$p_title" 18)" "held at $p_have"
    done < "$WORK/paks.tsv"
    ui_row_action "Check again" "check"
    ui_rows_finish "$LIST"
}

do_check() { ERR=""; ui_run "Checking for updates…" check || ui_message "$ERR" 4; }

install_baseos() {
    ui_confirm "Install BaseOS $B_LATEST?\n\n$((SIZE / 1048576)) MB to download, charger plugged in.\nGames, saves and settings stay in place." "INSTALL" || return 0
    ERR=""
    if ! ui_run "Downloading and verifying BaseOS $B_LATEST…" download; then
        echo "refused: $ERR"
        ui_message "$ERR" 5
        return 0
    fi
    if ui_confirm "BaseOS $B_LATEST is ready.\n\nRestart to install it? Do not switch off\nor unplug during the installation." "RESTART"; then
        touch /tmp/reboot
        exit 0
    fi
    ui_message "The file stays on the SD card:\nBaseOS $B_LATEST installs at the next boot." 5
}

updater_ready() {
    on_power || { ui_message "Plug in the charger:\nthe update must run on mains power." 4; return 1; }
    ! sync_running || { ui_message "A Save Sync sync is running:\ntry again in a minute." 4; return 1; }
}
run_updater() { (cd "$(dirname "$UPDATER")" && sh "$UPDATER"); }

prepare_mods() {
    ERR=""
    if ui_run "Preparing the modifications for NextUI $N_LATEST…" prepare; then
        ui_message "NextUI $N_LATEST is prepared:\nyour modifications come back\nat the first boot after the update." 4
    else
        ui_message "$ERR" 5
    fi
}

open_updater() {
    updater_ready || return 0
    ERR=""
    [ ! -d "$MODS/$N_BUILD" ] && can_fetch && ! ui_run "Preparing the modifications for NextUI $N_LATEST…" prepare &&
        ui_message "Preparation failed:\n$ERR" 5
    if [ -d "$MODS/$N_BUILD" ]; then
        msg="NextUI $N_LATEST is prepared:\nyour modifications come back\nat the first boot after the update."
    else
        msg="NextUI $N_LATEST is NOT prepared.\n\nAfter the update: no status dial, MENU opens\nthe Quick Menu. Prepare it first: Prepare,\nor on the Mac (nextui/build.sh latest,\nmac/install-nextui.sh)."
    fi
    ui_confirm "$msg" "INSTALL" || return 0
    run_updater
}

# Mise à jour d'un pak (id) ou de tous (all) par la commande pak.
update_paks() {
    if [ "$1" = all ]; then
        title="all paks ($(paks_count update))"
        set --
    else
        title=$(awk -F'\t' -v id="$1" '$1 == id { print $3 " " $4 " → " $5 }' "$WORK/paks.tsv")
    fi
    ui_confirm "Update $title?" "UPDATE" || return 0
    if ui_run "Updating $title…" "$PAK" upgrade "$@" > "$WORK/pak-upgrade.txt" 2>&1; then
        cat "$WORK/pak-upgrade.txt"
        ui_message "$(grep -E ' (updated|installed) \(' "$WORK/pak-upgrade.txt" | tail -n 4)" 3
    else
        cat "$WORK/pak-upgrade.txt"
        ui_message "Update failed:\n$(grep '^error' "$WORK/pak-upgrade.txt" | tail -n 3 | cut -c 1-120)" 6
    fi
    paks_check
}

# --- points d'entrée de test (stdout, sans brique) -------------------------------------------
case "${1:-}" in
--check) check || { echo "ERR=$ERR"; exit 1; }
    echo "baseos installed=$B_INSTALLED model=$MODEL latest=$B_LATEST asset=$ASSET size=$SIZE digest=$DIGEST"
    newer "$B_LATEST" "$B_INSTALLED" && echo baseos_newer=yes || echo baseos_newer=no
    echo "nextui installed=$N_INSTALLED latest=$N_LATEST build=$N_BUILD mods=$(mods_state)"
    nextui_newer && echo nextui_newer=yes || echo nextui_newer=no
    echo "paks update=$(paks_count update) held=$(paks_count held) error=$P_ERR"
    exit 0 ;;
--download) check && download || { echo "ERR=$ERR"; exit 1; }; exit 0 ;;
--prepare) check && nextui_newer && can_fetch && prepare || { echo "ERR=${ERR:-nothing to prepare}"; exit 1; }; exit 0 ;;
--list-only) check >/dev/null 2>&1; build_list; cat "$LIST"; exit 0 ;;
esac

paplib_log "Updates"
paplib_have minui-list || { ui_message "Updates: minui-list is missing." 4; exit 1; }
# Updater and Pak Store stay hidden: their work goes through this pak.
[ -f "$PAK" ] && "$PAK" hide Updater "Pak Store"
do_check
while :; do
    build_list
    ui_menu "$LIST" "Updates" || break
    action=$(ui_action_at)
    case "$action" in
    notes:baseos) ui_read "$WORK/baseos.notes" "BaseOS $B_LATEST" ;;
    notes:nextui) ui_read "$WORK/nextui.notes" "NextUI $N_LATEST" ;;
    install) [ -n "$MODEL" ] && install_baseos || ui_message "Unknown BaseOS model (/etc/baseos-release):\ncannot update." 5 ;;
    prepare) prepare_mods ;;
    updater) open_updater ;;
    updater-open) updater_ready && run_updater ;;
    pak:*) update_paks "${action#pak:}" ;;
    check) do_check ;;
    esac
done
