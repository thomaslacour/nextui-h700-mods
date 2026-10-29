#!/bin/sh
# check-updates.sh — vérifie les mises à jour BaseOS et NextUI.
#
# DÉTECTION, plus une préparation. Ce script n'installe rien, ne redémarre
# jamais et n'écrit aucune donnée de jeu. Il interroge l'API GitHub, compare
# avec ce qui est installé, puis écrit une fiche d'état :
#
#   /mnt/SDCARD/.userdata/<plateforme>/update-check/state
#
# C'est cette fiche que lit le launcher (badge en haut du menu principal, voir
# nextui/nextui-update-badge.patch). C'est la même séparation que Base Jumper :
# la détection est silencieuse et périodique, l'installation reste une action
# explicite de l'utilisateur (pak Updates).
#
# Seule écriture en plus : si une nouvelle NextUI est sortie et que nos
# modifications ne sont pas encore en réserve, nextui-mods/fetch.sh télécharge
# celle que GitHub Actions a compilée (vérifiée par SHA-256) dans
# nextui-mods/<build>/. Rien n'est posé : la garde ne s'en sert qu'au premier
# démarrage de cette version, après l'installation choisie dans Updates.
#
# Usage : ./check-updates.sh          # vérifie et écrit la fiche
#         ./check-updates.sh --print  # affiche la fiche courante
set -u

PLATFORM="${PLATFORM:-h700}"
SDCARD="${SDCARD_PATH:-/mnt/SDCARD}"
STATE_DIR="${UPDATE_CHECK_DIR:-$SDCARD/.userdata/$PLATFORM/update-check}"
STATE="$STATE_DIR/state"
LOG="$STATE_DIR/last-run.log"
TMP="${TMPDIR:-/tmp}/update-check.$$"

BASEOS_REPO="${BASEOS_REPO:-pvaibhav/BaseOS}"
NEXTUI_REPO="${NEXTUI_REPO:-LoveRetro/NextUI}"
MODS="${NEXTUI_MODS_DIR:-$SDCARD/.userdata/$PLATFORM/nextui-mods}"
# Chemins surchargeables : c'est ce qui permet de tester le script hors console
# (voir tests/test-update-check.sh) sans réseau ni carte SD.
VERSION_FILE="${NEXTUI_VERSION_FILE:-$SDCARD/.system/version.txt}"
RELEASE_FILE="${BASEOS_RELEASE_FILE:-/etc/baseos-release}"
ROUTE_FILE="${ROUTE_FILE:-/proc/net/route}"

say() { printf '%s\n' "$*"; }
log() { printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" >> "$LOG" 2>/dev/null || true; }

if [ "${1:-}" = --print ]; then
    [ -f "$STATE" ] && cat "$STATE" || say "(no state yet: run ./check-updates.sh)"
    exit 0
fi

mkdir -p "$STATE_DIR" || exit 1
mkdir -p "$TMP" || exit 1
trap 'rm -rf "$TMP"' EXIT

curl_bin() {
    if [ -x "$SDCARD/.system/$PLATFORM/bin/curl" ]; then
        printf '%s\n' "$SDCARD/.system/$PLATFORM/bin/curl"
    elif command -v curl >/dev/null 2>&1; then
        command -v curl
    fi
}
CURL="$(curl_bin)"
[ -n "$CURL" ] || { log "curl not found"; exit 1; }

# Réseau : une route par défaut suffit. Sans réseau on ne touche PAS à la fiche
# existante (on ne veut pas effacer un badge encore valable).
if ! awk '$2 == "00000000" { found = 1 } END { exit !found }' "$ROUTE_FILE" 2>/dev/null; then
    log "no network: previous state kept"
    exit 0
fi

# --- BaseOS ------------------------------------------------------------------
baseos_field() { sed -n "s/^$1=//p" "$RELEASE_FILE" 2>/dev/null | tr -d '"'; }
version_num() {
    case "$1" in '' | *[!0-9.]*) return 1 ;; esac
    printf '%s' "$1" | awk -F. '{printf "%d%03d%03d", $1, $2, $3}'
}
# Renvoie "nom taille url" pour chaque asset de la réponse JSON.
list_assets() {
    awk -F'"' '
        $2 == "assets" { in_assets = 1; next }
        !in_assets { next }
        $2 == "name" { name = $4 }
        $2 == "browser_download_url" { print name, $4 }
    ' "$1"
}
tag_of() { sed -n 's/^ *"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' "$1" | head -n 1; }

# Valeurs de la fiche précédente : gardées pour une source qui ne répond pas
# (réseau pas encore prêt au démarrage, limite de l'API…), pour que le badge
# ne disparaisse pas sur un simple échec.
prev() { sed -n "s/^$1=//p" "$STATE" 2>/dev/null | head -n 1; }

BOS_INSTALLED="$(baseos_field BASEOS_VERSION)"
BOS_TARGET="$(baseos_field BASEOS_TARGET)"
BOS_LATEST=""; BOS_UPDATE=0
if "$CURL" -sf --connect-timeout 10 -m 25 -o "$TMP/bos.json" \
        "https://api.github.com/repos/$BASEOS_REPO/releases/latest"; then
    BOS_LATEST="$(tag_of "$TMP/bos.json")"
    if [ -n "$BOS_TARGET" ] && list_assets "$TMP/bos.json" | awk -v t="baseos-$BOS_TARGET-" 'index($1,t)==1 && $1 ~ /\.bosupd$/' | grep -q .; then
        have="$(version_num "$BOS_INSTALLED" || true)"
        want="$(version_num "$BOS_LATEST" || true)"
        if [ -n "$have" ] && [ -n "$want" ] && [ "$have" -lt "$want" ] 2>/dev/null; then
            BOS_UPDATE=1
        fi
    fi
    log "BaseOS installed=${BOS_INSTALLED:-?} target=${BOS_TARGET:-?} latest=${BOS_LATEST:-?} update=$BOS_UPDATE"
else
    BOS_LATEST="$(prev baseos_latest)"; BOS_UPDATE="$(prev baseos_update)"
    [ "$BOS_LATEST" = unknown ] && BOS_LATEST=""
    log "BaseOS: GitHub request failed (previous state kept)"
fi

# --- NextUI ------------------------------------------------------------------
# version.txt : ligne 1 = identifiant de build (ex. NextUI-20261013-0).
NUI_INSTALLED="$(sed -n '1p' "$VERSION_FILE" 2>/dev/null | tr -d ' \r')"
NUI_LATEST=""; NUI_LATEST_BUILD=""; NUI_UPDATE=0
if "$CURL" -sf --connect-timeout 10 -m 25 -o "$TMP/nui.json" \
        "https://api.github.com/repos/$NEXTUI_REPO/releases/latest"; then
    NUI_LATEST="$(tag_of "$TMP/nui.json")"
    # Identifiant de build de la release (NextUI-<date>-<n>) : c'est le nom
    # de la réserve que nextui/guard.sh cherche après une mise à jour.
    NUI_LATEST_BUILD="$(list_assets "$TMP/nui.json" | sed -n 's/^\(NextUI-[0-9]*-[0-9]*\)-.*/\1/p' | head -n 1)"
    if [ -n "$NUI_INSTALLED" ]; then
        # La release porte des assets nommés d'après le build (…-all.zip, …-base.zip).
        # Si l'un d'eux commence par notre build installé, on est à jour.
        if ! list_assets "$TMP/nui.json" | awk -v id="$NUI_INSTALLED" 'index($1,id)==1 { found=1 } END { exit !found }'; then
            NUI_UPDATE=1
        fi
    fi
    log "NextUI installed=${NUI_INSTALLED:-?} latest=${NUI_LATEST:-?} update=$NUI_UPDATE"
else
    NUI_LATEST="$(prev nextui_latest)"; NUI_LATEST_BUILD="$(prev nextui_latest_build)"; NUI_UPDATE="$(prev nextui_update)"
    [ "$NUI_LATEST" = unknown ] && NUI_LATEST=""
    [ "$NUI_LATEST_BUILD" = unknown ] && NUI_LATEST_BUILD=""
    log "NextUI: GitHub request failed (previous state kept)"
fi

# --- Fiche d'état (écriture atomique) ---------------------------------------
{
    printf 'checked=%s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')"
    printf 'baseos_installed=%s\n' "${BOS_INSTALLED:-unknown}"
    printf 'baseos_target=%s\n' "${BOS_TARGET:-unknown}"
    printf 'baseos_latest=%s\n' "${BOS_LATEST:-unknown}"
    printf 'baseos_update=%s\n' "${BOS_UPDATE:-0}"
    printf 'nextui_installed=%s\n' "${NUI_INSTALLED:-unknown}"
    printf 'nextui_latest=%s\n' "${NUI_LATEST:-unknown}"
    printf 'nextui_latest_build=%s\n' "${NUI_LATEST_BUILD:-unknown}"
    printf 'nextui_update=%s\n' "${NUI_UPDATE:-0}"
} > "$STATE.tmp" || exit 1
mv -f "$STATE.tmp" "$STATE"

# --- Préparation de nos modifications pour la nouvelle NextUI ----------------
if [ "$NUI_UPDATE" = 1 ] && [ -n "$NUI_LATEST_BUILD" ] && [ ! -d "$MODS/$NUI_LATEST_BUILD" ] && [ -f "$MODS/fetch.sh" ]; then
    out=$(NEXTUI_MODS_DIR="$MODS" sh "$MODS/fetch.sh" "$NUI_LATEST_BUILD" "v$NUI_LATEST" 2>&1)
    log "NextUI modifications: $(printf '%s' "$out" | tail -n 1)"
fi

if [ "$BOS_UPDATE" = 1 ] || [ "$NUI_UPDATE" = 1 ]; then
    say "Update available:$( [ "$BOS_UPDATE" = 1 ] && printf ' BaseOS %s' "${BOS_LATEST:-?}" )$( [ "$NUI_UPDATE" = 1 ] && printf ' NextUI %s' "${NUI_LATEST:-?}" )"
else
    say 'Everything is up to date.'
fi
