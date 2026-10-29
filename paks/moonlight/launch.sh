#!/bin/sh
# Moonlight — streaming de jeux depuis un PC (Apollo / Sunshine) vers la console.
#
# Client : moonlight-embedded du port PortMaster « Moonlight New », plus les
# bibliothèques absentes de BaseOS (moonlight/build.sh), dans client/. Ce pak
# remplace le lanceur LÖVE du port par des écrans NextUI natifs :
#   - hôte choisi sans clavier : PC déjà connus, scan du réseau local (port
#     47989, /serverinfo d'Apollo/Sunshine) ou appareils Tailscale ;
#   - appairage : le PIN s'affiche, à saisir dans l'interface web d'Apollo ;
#   - système « Moonlight » du menu principal : une entrée par appli visible de
#     l'hôte (Roms/Moonlight (MOONLIGHT)/<appli>.ml, lancée par Emus/h700/MOONLIGHT.pak,
#     qui appelle ce script avec --launch) ; ici, chaque appli se montre ou se
#     cache (hidden.txt) ;
#   - 4 préréglages de qualité au lieu de 5 réglages (H.264 imposé : le H700
#     décode en logiciel, le HEVC ne suit pas).
# Sortie d'un stream : SELECT + START + L1 + R1 (combinaison de moonlight).
#
# Données : .userdata/h700/moonlight/ (keys/ = certificat client, à ne jamais
# partager ; hosts.txt « nom|ip » ; host.txt = hôte courant ; quality.txt ;
# buttons.txt ; hidden.txt = applis cachées du système Moonlight ; macs.txt
# « nom|mac » pour le Wake-on-LAN).
PAK_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$PAK_DIR/lib/paplib.sh"
paplib_init
DATA="${MOONLIGHT_DATA:-$USERDATA_PATH/moonlight}"
KEYS="$DATA/keys"
HOSTS="$DATA/hosts.txt"
HOST_FILE="$DATA/host.txt"
MACS="$DATA/macs.txt" # « nom|mac » : adresse matérielle de chaque hôte (Wake-on-LAN)
ARP="${MOONLIGHT_ARP:-/proc/net/arp}" # table ARP du noyau (MAC des voisins)
QUALITY_FILE="$DATA/quality.txt"
BUTTONS_FILE="$DATA/buttons.txt"
PLAYERS_FILE="$DATA/players.txt" # 1 = manette externe seule, 2 = console + manette
HIDDEN="$DATA/hidden.txt"
SYSTEM_DIR="${MOONLIGHT_ROMS:-$SDCARD_PATH/Roms/Moonlight (MOONLIGHT)}"
APPS="$DATA/apps.txt"
MAPPING="$DATA/gamecontrollerdb.txt"
PAD_DB="${MOONLIGHT_PAD_DB:-$USERDATA_PATH/gamecontrollerdb.txt}" # base SDL des manettes
INPUT_DEVICES="${MOONLIGHT_INPUT_DEVICES:-/proc/bus/input/devices}"
CLIENT="${MOONLIGHT_CLIENT:-$PAK_DIR/client/moonlight}"
WOL="${MOONLIGHT_WOL:-$PAK_DIR/client/wol}" # paquet magique (paks/moonlight/wol)
PORTS_LIB="$SDCARD_PATH/Emus/$PLATFORM/PORTS.pak/lib"
KEYBOARD="${MOONLIGHT_KEYBOARD:-$PAK_DIR/client/minui-keyboard}" # on-screen keyboard (IP entry)
TAILSCALE="${TAILSCALE_BIN:-$SDCARD_PATH/Tools/$PLATFORM/Tailscale.pak/bin/arm64/tailscale}"
CURL="$SDCARD_PATH/.system/$PLATFORM/bin/curl"
[ -x "$CURL" ] || CURL=curl
LIST="$TMP/moonlight-list.json"
TEXT="$TMP/moonlight-text"
trap 'rm -f "$LIST" "$TEXT" "$TEXT".*; paplib_cleanup' EXIT INT TERM HUP
mkdir -p "$KEYS"

# Manette de la RG35XX SP (« ANBERNIC-keys », event1). Deux dispositions
# (Settings → Confirm button, buttons.txt) :
#   labels : par étiquette, le A de la console envoie A, X envoie X (défaut,
#            testé en jeu) ;
#   xbox   : par position, comme une manette Xbox : le bouton du bas (B) envoie
#            A, celui de droite (A) envoie B, gauche (Y) → X, haut (X) → Y — pour
#            les jeux PC qui attendent l'action principale en bas (Spyro).
# Index SDL = ordre des codes evdev présents :
# 304 A→b0, 305 B→b1, 306 Y→b2, 307 X→b3, 308 L1→b4, 309 R1→b5, 310 SELECT→b6,
# 311 START→b7, 312 MENU→b8, 314 L2→b9, 315 R2→b10 (codes de NextUI h700).
# GUID calculé par SDL et moonlight : bus 0019, 0001:0001, version 0100.
buttons() { [ "$(cat "$BUTTONS_FILE" 2>/dev/null)" = xbox ] && echo xbox || echo labels; }
write_mapping() {
    case "$(buttons)" in
    xbox) face="a:b1,b:b0,x:b2,y:b3" ;;
    *) face="a:b0,b:b1,x:b3,y:b2" ;;
    esac
    # Manettes externes connectées (8BitDo, Xbox…) : leurs lignes de la base
    # SDL (posée par install-nextui.sh) ; la console n'est alors pas décrite,
    # pour que la manette soit le joueur 1 sur le PC (moonlight ne gère que les
    # manettes décrites). Sans manette externe : la console seule.
    # Players = 2 : les deux sont décrites ; SDL les numérote dans l'ordre des
    # périphériques (console event1 = joueur 1, manette = joueur 2, comme minarch).
    pads=$(pad_mappings)
    console="19000000010000000100000000010000,ANBERNIC-keys,$face,back:b6,start:b7,guide:b8,leftshoulder:b4,rightshoulder:b5,lefttrigger:b9,righttrigger:b10,dpup:h0.1,dpdown:h0.4,dpleft:h0.8,dpright:h0.2,platform:Linux,"
    if [ -z "$pads" ]; then
        printf '%s\n' "$console" > "$MAPPING"
    elif [ "$(players)" = 2 ]; then
        printf '%s\n' "$console" "$pads" > "$MAPPING"
    else
        printf '%s\n' "$pads" > "$MAPPING"
    fi
}
players() { [ "$(cat "$PLAYERS_FILE" 2>/dev/null)" = 2 ] && echo 2 || echo 1; }
# pad_mappings : lignes de la base pour les manettes externes présentes. GUID
# SDL d'un périphérique Linux = bus, vendeur, produit, version (16 bits petit
# boutiste, suivis de 0000) ; octets 2-3 libres (CRC du nom, SDL ≥ 2.26).
pad_mappings() {
    [ -r "$PAD_DB" ] || return 0
    awk '
        function le(h) { h = sprintf("%04s", h); gsub(/ /, "0", h); return tolower(substr(h, 3, 2) substr(h, 1, 2)) }
        /^I:/ { n = split($0, f, /[ =]+/); for (i = 2; i < n; i += 2) v[f[i]] = f[i + 1] }
        /^H:/ && /[= ]js[0-9]/ && v["Bus"] != "0019" {
            print "^" le(v["Bus"]) "...." le(v["Vendor"]) "0000" le(v["Product"]) "0000" le(v["Version"]) "0000,"
        }' "$INPUT_DEVICES" 2>/dev/null | while IFS= read -r re; do
        grep -i -- "$re" "$PAD_DB" | tail -n 1 # la dernière l'emporte (corrections)
    done
}
write_mapping

# --- client ------------------------------------------------------------------------------
# ml <commande…> : moonlight avec ses bibliothèques (pak, système, PortMaster).
# Sortie non tamponnée (libunbuffer.so) : le journal du pak reste complet même
# si moonlight est interrompu, et le PIN de « pair » se lit tout de suite.
ml() {
    LD_LIBRARY_PATH="$PAK_DIR/client/libs:$SDCARD_PATH/.system/$PLATFORM/lib:$PORTS_LIB:$LD_LIBRARY_PATH" \
        LD_PRELOAD="${ML_PRELOAD-$PAK_DIR/client/libs/libunbuffer.so}" "$CLIENT" "$@" -keydir "$KEYS"
}
# bounded <secondes> <commande…> : timeout de BusyBox (absent du Mac des tests).
bounded() { if command -v timeout >/dev/null 2>&1; then timeout "$@"; else shift; "$@"; fi; }
ml_log() { ml "$@" 2>&1 | grep -v '^LIBGL'; } # sans le bavardage de gl4es
host_name() { cut -d'|' -f1 "$HOST_FILE" 2>/dev/null; }
host_ip() { cut -d'|' -f2 "$HOST_FILE" 2>/dev/null; }

# Préréglages : id|libellé|largeur|hauteur|i/s|kb/s|remote
presets() {
    cat <<'EOF'
low|Low · 480p 30 · 3 Mb|640|480|30|3000|auto
normal|Normal · 480p 60 · 6 Mb|640|480|60|6000|auto
sharp|Sharp · 720p 60 · 10 Mb|960|720|60|10000|auto
remote|Remote · 480p 30 · 2 Mb|640|480|30|2000|yes
EOF
}
quality() { q=$(cat "$QUALITY_FILE" 2>/dev/null); presets | grep -q "^$q|" && echo "$q" || echo normal; }
preset_field() { presets | awk -F'|' -v q="$(quality)" -v n="$1" '$1 == q { print $n }'; }
quality_short() { preset_field 2 | sed 's/^[^·]*· //'; }

# refresh : interroge l'hôte (moonlight list) -> STATUS = paired | not paired |
# offline | no host ; liste des jeux dans $APPS.
refresh() {
    STATUS="no host"
    ip=$(host_ip)
    [ -n "$ip" ] || return 0
    # Sonde rapide d'abord, puis « list » borné : moonlight peut attendre des
    # minutes un PC en veille, et l'écran « Connecting… » n'a pas de bouton.
    name=$(probe "$ip")
    [ -n "$name" ] || { echo "$ip: no answer on port 47989"; STATUS="offline"; return 0; }
    # Host entered by IP address: it takes its real name once it answers.
    [ "$(host_name)" != "$ip" ] || save_host "$name" "$ip"
    remember_mac "$ip" # the PC answers: keep its MAC (Wake-on-LAN) up to date
    out=$(bounded "${ML_TIMEOUT:-15}" sh "$PAK_DIR/launch.sh" --ml list "$ip" 2>&1 | grep -v '^LIBGL')
    printf '%s\n' "$out"
    case "$out" in
    *"pair with"* | *"not paired"* | *"Not paired"*) STATUS="not paired" ;;
    *"Can't connect"* | *"Couldn't connect"* | *"Failed to connect"*) STATUS="offline" ;;
    *[0-9]". "*) STATUS="paired"; printf '%s\n' "$out" | sed -n 's/^[0-9][0-9]*\. //p' > "$APPS"; sync_entries ;;
    *) STATUS="offline" ;; # pas de réponse dans le délai
    esac
}

# --- Wake-on-LAN ---------------------------------------------------------------------------
# MAC stored by host NAME (still valid if the PC changes IP), read again on each
# refresh where the PC answers, so it follows a new network card. Sources: the
# console's ARP table (real MAC of the IP that just answered), else the host's
# /serverinfo (HTTPS API first: an unpaired client may get a masked MAC).
# by_name <name> <file>: "name|…" lines whose name is exactly <name>.
by_name() { awk -F'|' -v n="$1" '$1 == n' "$2" 2>/dev/null; }
host_mac() { [ -n "$(host_name)" ] && by_name "$(host_name)" "$MACS" | sed -n 's/.*|//p' | head -n 1; }
arp_mac() { awk -v ip="$1" '$1 == ip && $3 == "0x2" && $4 != "00:00:00:00:00:00" { print $4; exit }' "$ARP" 2>/dev/null; }
remember_mac() { # <ip>
    name=$(host_name)
    [ -n "$name" ] || return 0
    mac=$({ arp_mac "$1"
            { host_api serverinfo; "$CURL" -s --connect-timeout 1 -m 2 "http://$1:47989/serverinfo" 2>/dev/null; } |
                sed -n 's:.*<mac>\([0-9A-Fa-f:]*\)</mac>.*:\1:p'; } |
        grep -i -v '^00:00:00:00:00:00$' | grep -i -m 1 '^[0-9a-f][0-9a-f]\(:[0-9a-f][0-9a-f]\)\{5\}$' |
        tr 'A-F' 'a-f')
    [ -n "$mac" ] || return 0
    [ "$(host_mac)" = "$mac" ] && return 0
    { awk -F'|' -v n="$name" '$1 != n' "$MACS" 2>/dev/null; printf '%s|%s\n' "$name" "$mac"; } > "$MACS.tmp" && mv -f "$MACS.tmp" "$MACS"
    echo "MAC of $name: $mac"
}
# relocate_host: the PC may have a new IP (DHCP); look on the local network for
# a host with the same name and switch to it. 0 if found.
relocate_host() {
    scan_lan >/dev/null 2>&1
    relocate=$(by_name "$(host_name)" "$TEXT.found" | head -n 1)
    [ -n "$relocate" ] || return 1
    [ "${relocate#*|}" = "$(host_ip)" ] && return 0
    echo "$(host_name) moved: $(host_ip) -> ${relocate#*|}"
    awk -F'|' -v n="$(host_name)" -v i="$(host_ip)" '!($1 == n && $2 == i)' "$HOSTS" > "$HOSTS.tmp" 2>/dev/null &&
        mv -f "$HOSTS.tmp" "$HOSTS"
    save_host "$(host_name)" "${relocate#*|}"
}
# wake_send <mac> : paquet magique en diffusion générale et sur le réseau du Wi-Fi.
wake_send() {
    "$WOL" "$1" 255.255.255.255 $(ip -4 addr show wlan0 2>/dev/null | sed -n 's/.* brd \([0-9.]*\).*/\1/p') 2>&1
}
# wake_host <message>: wake the host and wait for its server (WAKE_TIMEOUT s,
# 60 by default; packet resent every 6 s; after 20 s without an answer, look
# for the PC on the network in case its IP changed). 0 = it answers, 1 = timed
# out, 2 = cancelled (B on the waiting screen).
wake_host() {
    wake_mac=$(host_mac)
    [ -n "$wake_mac" ] || return 1
    wake_ui=""
    if paplib_have minui-presenter; then
        killall -9 minui-presenter >/dev/null 2>&1 || true
        minui-presenter --message "$1" --timeout -1 --cancel-show --cancel-text "CANCEL" >/dev/null 2>&1 &
        wake_ui=$!
    fi
    wake_start=$(date +%s); wake_last=0; wake_rc=1; wake_moved=0
    while [ $(($(date +%s) - wake_start)) -lt "${WAKE_TIMEOUT:-60}" ]; do
        if [ $(($(date +%s) - wake_last)) -ge 6 ]; then
            wake_send "$wake_mac"; wake_last=$(date +%s)
        fi
        [ -z "$(probe "$(host_ip)")" ] || { wake_rc=0; break; }
        # still silent after 20 s: maybe a new IP (DHCP); look once
        if [ "$wake_moved" = 0 ] && [ $(($(date +%s) - wake_start)) -ge 20 ]; then
            wake_moved=1
            relocate_host && [ -n "$(probe "$(host_ip)")" ] && { wake_rc=0; break; }
        fi
        if [ -n "$wake_ui" ] && ! kill -0 "$wake_ui" 2>/dev/null; then wake_rc=2; break; fi
        sleep 1
    done
    [ -z "$wake_ui" ] || kill -9 "$wake_ui" 2>/dev/null
    echo "wake $(host_name): rc=$wake_rc after $(($(date +%s) - wake_start)) s"
    return "$wake_rc"
}
wake_failed() { ui_message "$(host_name) did not wake up.\n\nCheck Wake-on-LAN in the\nBIOS and network settings." 5; }
wake_screen() {
    wake_host "Waking $(host_name)…\n\nUp to ${WAKE_TIMEOUT:-60} s"
    [ $? -ne 1 ] || wake_failed
}

# --- hôtes --------------------------------------------------------------------------------
# probe <ip> : nom de l'hôte Apollo/Sunshine qui répond, sinon rien.
probe() {
    "$CURL" -s --connect-timeout 1 -m 2 "http://$1:47989/serverinfo" 2>/dev/null |
        sed -n 's:.*<hostname>\([^<]*\)</hostname>.*:\1:p' | head -n 1
}
# save_host <nom> <ip> : mémorise (une entrée par ip) et sélectionne.
save_host() {
    { grep -v "|$2\$" "$HOSTS" 2>/dev/null; printf '%s|%s\n' "$1" "$2"; } > "$HOSTS.tmp" && mv -f "$HOSTS.tmp" "$HOSTS"
    printf '%s|%s\n' "$1" "$2" > "$HOST_FILE"
}
# lan_prefix : « 192.168.1 » pour un réseau /24 sur le Wi-Fi.
lan_prefix() { ip -4 route 2>/dev/null | sed -n 's:^\([0-9]*\.[0-9]*\.[0-9]*\)\.0/24 .*:\1:p' | head -n 1; }
# probe_all <fichier d'ip> : « nom|ip » des hôtes qui répondent, 32 à la fois.
# On n'attend que ses propres sondes : un « wait » nu attendrait aussi le
# message de ui_run (minui-presenter en arrière-plan, sans fin).
probe_all() {
    : > "$TEXT.found"
    pids=""
    while read -r a; do
        (name=$(probe "$a"); [ -z "$name" ] || printf '%s|%s\n' "$name" "$a" >> "$TEXT.found") &
        pids="$pids $!"
        set -- $pids
        [ $# -lt 32 ] || { wait $pids; pids=""; }
    done < "$1"
    [ -z "$pids" ] || wait $pids
    sort -t'|' -k2 "$TEXT.found"
}
scan_lan() {
    p=$(lan_prefix)
    [ -n "$p" ] || { echo "no /24 network" >&2; return 1; }
    i=1; while [ $i -le 254 ]; do echo "$p.$i"; i=$((i + 1)); done > "$TEXT.ips"
    probe_all "$TEXT.ips"
}
scan_tailscale() {
    [ -x "$TAILSCALE" ] || return 1
    "$TAILSCALE" status 2>/dev/null | awk 'NR > 1 && $1 ~ /^100\./ && $0 !~ /offline/ { print $1 }' > "$TEXT.ips"
    probe_all "$TEXT.ips" | sed 's/|/ (VPN)|/'
}
# found_pick <titre> : choisit parmi $TEXT.list (« nom|ip »), mémorise.
found_pick() {
    [ -s "$TEXT.list" ] || return 1
    awk -F'|' '{ print $1 " · " $2 "|" $0 }' "$TEXT.list" > "$TEXT.menu"
    choice=$(ui_pick "$TEXT.menu" "$1" "CHOOSE") || return 0
    save_host "${choice%|*}" "${choice#*|}"
}
# Manual entry: on-screen keyboard prefilled with the current address. Fallback
# without the keyboard: "Network" (a.b.c) and "Address" (.d) cycled with
# left / right; networks offered: the Wi-Fi /24, those of known hosts, then
# common home ones.
ip_networks() {
    { lan_prefix; cut -d'|' -f2 "$HOSTS" 2>/dev/null | sed -n 's/^\([0-9]*\.[0-9]*\.[0-9]*\)\.[0-9]*$/\1/p'
      printf '%s\n' 192.168.1 192.168.0 10.0.0; } | awk 'NF && !seen[$0]++'
}
# ip_option_at <state> <item>: selected option index of an item (0-based).
ip_option_at() {
    awk -v want="$2" '{
        line = $0
        if (depth == 1 && line ~ /\{/) item++
        if (depth == 2 && item - 1 == want && match(line, /"selected"[ \t]*:[ \t]*[0-9]+/)) {
            s = substr(line, RSTART, RLENGTH); sub(/.*:[ \t]*/, "", s); print s + 0; exit
        }
        depth += gsub(/\{/, "{", line) - gsub(/\}/, "}", line)
    }' "$1" 2>/dev/null
}
# valid_ip <text>: four numbers 0-255 separated by dots.
valid_ip() {
    printf '%s\n' "$1" | awk -F. 'NF != 4 { exit 1 } { for (i = 1; i <= 4; i++) if ($i !~ /^[0-9]+$/ || $i > 255) exit 1 }'
}
enter_ip() {
    [ -x "$KEYBOARD" ] || { pick_ip; return; }
    value=$(host_ip)
    while :; do
        rm -f "$TEXT.kb"
        "$KEYBOARD" --title "IP address" --initial-value "$value" --write-location "$TEXT.kb" \
            --disable-auto-sleep >&2 || return 1
        value=$(tr -d ' \r\n' < "$TEXT.kb" 2>/dev/null)
        valid_ip "$value" && break
        ui_message "Invalid IP address." 2
    done
    try_ip "$value"
}
pick_ip() {
    ip_networks > "$TEXT.nets"
    cur=$(host_ip)
    net_at=$(grep -nxF -- "${cur%.*}" "$TEXT.nets" | cut -d: -f1)
    addr_at=$(printf '%s' "${cur##*.}" | grep -x '[0-9]*' || echo 1)
    [ "$addr_at" -ge 1 ] 2>/dev/null && [ "$addr_at" -le 254 ] || addr_at=1
    {
        printf '{\n  "selected": 1,\n  "items": [\n    {"name": "Network", "selected": %s, "options": [' "$(( ${net_at:-1} - 1 ))"
        awk 'NR > 1 { printf ", " } { printf "\"%s\"", $0 }' "$TEXT.nets"
        printf ']},\n    {"name": "Address", "selected": %s, "options": [' "$((addr_at - 1))"
        i=1; while [ $i -le 254 ]; do [ $i = 1 ] || printf ', '; printf '".%s"' $i; i=$((i + 1)); done
        printf ']},\n    {"name": "Connect", "features": {"show_confirm": true}}\n  ]\n}\n'
    } > "$TEXT.ip.json"
    ui_menu "$TEXT.ip.json" "IP address" "$TEXT.ip.state" "CONNECT" "BACK" || return 1
    net=$(sed -n "$(( $(ip_option_at "$TEXT.ip.state" 0 | grep . || echo 0) + 1 ))p" "$TEXT.nets")
    try_ip "$net.$(( $(ip_option_at "$TEXT.ip.state" 1 | grep . || echo 0) + 1 ))"
}
# try_ip <ip>: select the host at that address (its name if it answers).
try_ip() {
    ip=$1
    name=$(ui_run "Connecting to $ip…" probe "$ip")
    if [ -n "$name" ]; then
        save_host "$name" "$ip"
    else
        # No answer (asleep PC, server stopped): a known address keeps its
        # host name (and so its MAC for Wake-on-LAN); a new one is kept under
        # its address and gets its name on the first refresh where it answers.
        ui_confirm "No answer from $ip.\n\nKeep it anyway?" "KEEP" || return 1
        name=$(awk -F'|' -v i="$ip" '$2 == i { print $1; exit }' "$HOSTS" 2>/dev/null)
        save_host "${name:-$ip}" "$ip"
    fi
}
choose_host() {
    while :; do
        { [ -s "$HOSTS" ] && awk -F'|' '{ print $1 " · " $2 "|host:" $0 }' "$HOSTS"
          echo "Scan local network|scan"
          echo "Enter IP address|manual"
          [ -x "$TAILSCALE" ] && echo "Tailscale devices|tailscale"; } > "$TEXT.hosts"
        choice=$(ui_pick "$TEXT.hosts" "Host" "CHOOSE" "host:$(cat "$HOST_FILE" 2>/dev/null)") || return 0
        case "$choice" in
        host:*) printf '%s\n' "${choice#host:}" > "$HOST_FILE"; return 0 ;;
        scan)
            ui_run "Looking for hosts\non $(lan_prefix).x…" scan_lan > "$TEXT.list"
            found_pick "Found on Wi-Fi" && return 0
            ui_message "No host found.\n\nCheck the streaming server\nand firewall on the PC." 4 ;;
        manual) enter_ip && return 0 ;;
        tailscale)
            ui_run "Looking for hosts\non Tailscale…" scan_tailscale > "$TEXT.list"
            found_pick "Found on Tailscale" && return 0
            ui_message "No host found on Tailscale." 3 ;;
        esac
    done
}

# --- appairage ----------------------------------------------------------------------------
pair() {
    ip=$(host_ip)
    LD_PRELOAD="$PAK_DIR/client/libs/libunbuffer.so" ml pair "$ip" > "$TEXT.pair" 2>&1 &
    p=$!
    t=0
    while [ $t -lt 20 ] && kill -0 $p 2>/dev/null && ! grep -q 'PIN' "$TEXT.pair"; do sleep 1; t=$((t + 1)); done
    pin=$(sed -n 's/.*PIN[^0-9]*\([0-9][0-9][0-9][0-9]\).*/\1/p' "$TEXT.pair" | head -n 1)
    if [ -z "$pin" ]; then
        kill $p 2>/dev/null; cat "$TEXT.pair"
        ui_message "Cannot start pairing with $(host_name).\nSee Logs." 4; return 0
    fi
    minui-presenter --message "Enter this PIN in Apollo\n(web UI, PIN tab):\n\n$pin\n\nWaiting for $(host_name)…" \
        --timeout 0 --cancel-show --cancel-text "CANCEL" >/dev/null 2>&1 &
    q=$!
    while kill -0 $p 2>/dev/null && kill -0 $q 2>/dev/null; do sleep 1; done
    kill -9 $q 2>/dev/null
    if kill -0 $p 2>/dev/null; then kill $p 2>/dev/null; return 0; fi # annulé (B)
    cat "$TEXT.pair"
    if grep -qiE 'succes+fully paired|already paired' "$TEXT.pair"; then ui_message "Paired with $(host_name)." 2
    else ui_message "Pairing failed.\nCheck the PIN and try again." 4; fi
}
unpair() {
    ui_confirm "Unpair from $(host_name)?\n\nYou will need the PIN again." "UNPAIR" || return 0
    ml_log unpair "$(host_ip)"
    : > "$APPS"
    rm -f "$SYSTEM_DIR"/*.ml
}

# --- système « Moonlight » du menu principal -----------------------------------------------
# Une entrée par appli visible : <appli>.ml, contenu = titre exact (nom de
# fichier assaini ; extension de 4 lettres au plus, que NextUI masque). hidden.txt = applis cachées ; créé à la
# première liste avec tout sauf Steam Big Picture (le bureau, etc.) ; une
# appli ajoutée plus tard sur l'hôte (un lanceur de jeu, par exemple) est
# visible d'office. Sans liste (hôte injoignable), les entrées restent.
is_hidden() { grep -Fxq -- "$1" "$HIDDEN" 2>/dev/null; }
# Steam Big Picture comes first: NextUI sorts by file name and hides a leading
# "<digits>) " sort prefix (trimSortingMeta), so "0) Steam Big Picture.ml" is
# shown as "Steam Big Picture" at the top of the list.
entry_file() {
    case "$1" in "Steam Big Picture") entry_prefix="0) " ;; *) entry_prefix="" ;; esac
    printf '%s/%s%s.ml' "$SYSTEM_DIR" "$entry_prefix" "$(printf '%s' "$1" | tr '/:*?"<>|\\' '_')"
}
seed_hidden() {
    [ ! -f "$HIDDEN" ] && [ -s "$APPS" ] || return 0
    grep -Fxv 'Steam Big Picture' "$APPS" > "$HIDDEN" || true
}
sync_entries() {
    [ -s "$APPS" ] || return 0
    seed_hidden
    mkdir -p "$SYSTEM_DIR"
    : > "$TEXT.keep"
    while IFS= read -r app; do
        is_hidden "$app" && continue
        f=$(entry_file "$app")
        printf '%s\n' "$f" >> "$TEXT.keep"
        [ "$(cat "$f" 2>/dev/null)" = "$app" ] || { printf '%s\n' "$app" > "$f.tmp" && mv -f "$f.tmp" "$f"; }
    done < "$APPS"
    for f in "$SYSTEM_DIR"/*.ml "$SYSTEM_DIR"/*.moonlight; do # .moonlight : ancienne extension
        [ -e "$f" ] || continue
        grep -Fxq -- "$f" "$TEXT.keep" || rm -f "$f"
    done
}
# toggle_app <n> : montre ou cache la n-ième appli de l'hôte.
toggle_app() {
    app=$(sed -n "${1}p" "$APPS")
    [ -n "$app" ] || return 0
    seed_hidden
    if is_hidden "$app"; then
        { grep -Fxv -- "$app" "$HIDDEN" || true; } > "$HIDDEN.tmp" && mv -f "$HIDDEN.tmp" "$HIDDEN"
    else
        printf '%s\n' "$app" >> "$HIDDEN"
    fi
    sync_entries
}
# launch_entry <fichier .ml> : appel du système Moonlight (menu principal).
launch_entry() {
    app=$(head -n 1 "$1" 2>/dev/null)
    [ -n "$app" ] || { ui_message "Moonlight: empty entry." 3; return 0; }
    [ -n "$(host_ip)" ] || { ui_message "Moonlight has no host yet.\n\nOpen Tools → Moonlight first." 5; return 0; }
    if [ -z "$(probe "$(host_ip)")" ]; then
        if [ -z "$(host_mac)" ]; then
            ui_message "$(host_name) does not answer.\n\nIs the PC on, with Apollo running?" 5; return 0
        fi
        wake_host "$(host_name) is asleep.\nWaking it up, then streaming…"
        case $? in 1) wake_failed; return 0 ;; 2) return 0 ;; esac
    fi
    play "$app"
}

# --- stream -------------------------------------------------------------------------------
# host_api <serverinfo|applist> : API HTTPS de l'hôte avec le certificat du
# client (moonlight-embedded n'expose ni l'appli en cours ni les identifiants).
host_api() {
    "$CURL" -sk --connect-timeout 3 -m 6 --cert "$KEYS/client.pem" --key "$KEYS/key.pem" \
        "https://$(host_ip):47984/$1?uniqueid=$(cat "$KEYS/uniqueid.dat" 2>/dev/null)&uuid=$$" 2>/dev/null
}
current_game() { host_api serverinfo | sed -n 's:.*<currentgame>\([0-9]*\)</currentgame>.*:\1:p' | head -n 1; }
# apps_ids : « id|titre » de chaque appli de l'hôte.
apps_ids() {
    host_api applist | sed 's:<App>:\n<App>:g' |
        sed -n 's:.*<AppTitle>\([^<]*\)</AppTitle>.*<ID>\([0-9]*\)</ID>.*:\2|\1:p'
}
# before_play <jeu> : libgamestream REPREND toujours l'appli en cours sur le
# PC (currentgame ≠ 0), quelle que soit celle demandée. Si une autre tourne,
# proposer de la quitter d'abord ; 1 = annulé.
before_play() {
    case "$1" in *[Ss]hutdown* | *[Rr]estart* | *[Rr]eboot* | *[Ss]leep*)
        ui_confirm "$1 acts on the PC itself.\n\nStart it anyway?" "START" || return 1 ;;
    esac
    cur=$(current_game)
    [ -n "$cur" ] && [ "$cur" != 0 ] || return 0
    apps_ids > "$TEXT.ids"
    want=$(awk -F'|' -v t="$1" '$2 == t { print $1; exit }' "$TEXT.ids")
    [ "$cur" != "$want" ] || return 0 # même appli : reprise
    running=$(awk -F'|' -v i="$cur" '$1 == i { print $2; exit }' "$TEXT.ids")
    ui_confirm "${running:-Another app} is running on the PC.\n\nQuit it and start $1?" "QUIT AND START" || return 1
    echo "quit $running ($cur) before $1"
    ui_run "Closing ${running:-the running app}…" ml_log quit "$(host_ip)"
}
# stream_params : « largeur hauteur i/s kb/s remote » du préréglage courant.
stream_params() { presets | awk -F'|' -v q="$(quality)" '$1 == q { print $3, $4, $5, $6, $7 }'; }
play() {
    before_play "$1" || return 0
    minui-presenter --message "Starting $1…\n\nTo quit the stream, hold\nSELECT + START + L1 + R1" --timeout 3 >/dev/null 2>&1
    set -- "$1" $(stream_params)
    echo "stream \"$1\" ${2}x$3 $4 fps $5 kb/s remote=$6 -> $(host_ip)"
    write_mapping # manettes branchées depuis l'ouverture du pak
    export SDL_GAMECONTROLLERCONFIG="$(cat "$MAPPING")"
    ml stream -app "$1" -width "$2" -height "$3" -fps "$4" -bitrate "$5" -remote "$6" \
        -codec h264 -mapping "$MAPPING" "$(host_ip)" 2>&1 | grep -v '^LIBGL' | tee "$TEXT.stream"
    # Apollo : un client appairé après le premier n'a que « View Streams » et
    # « List Apps » ; sans « Launch Apps », il ne voit que le bureau.
    if grep -qi 'permission' "$TEXT.stream"; then
        ui_message "Apollo refused: missing permission.\n\nOn the PC: Apollo web UI, PIN tab,\nDevice Management, edit this console:\nallow Launch Apps and Controller input." 8
    fi
}

# --- écran --------------------------------------------------------------------------------
build_list() {
    ui_rows_start
    ui_row_info "Host" "$(host_name | grep . || echo 'choose…')" "host"
    case "$STATUS" in
    "not paired") ui_row_info "Status" "not paired"; ui_row_action "Pair" "pair" ;;
    offline) ui_row_info "Status" "offline" "refresh"
        [ -z "$(host_mac)" ] || ui_row_action "Wake PC" "wake" ;;
    paired) ui_row_info "Status" "paired" ;;
    esac
    if [ "$STATUS" = paired ]; then
        ui_row_header "Shown in Moonlight"
        if [ -s "$APPS" ]; then
            n=0
            while IFS= read -r app; do
                n=$((n + 1))
                ui_row_info "$(ui_short "$app" 24)" "$(is_hidden "$app" && echo Hidden || echo Shown)" "toggle:$n"
            done < "$APPS"
        else
            ui_row_info "No app on the host" ""
        fi
    fi
    ui_row_header "Settings"
    ui_row_info "Quality" "$(quality_short)" "quality"
    ui_row_info "Confirm button" "$([ "$(buttons)" = xbox ] && echo 'B, bottom' || echo 'A, right')" "buttons"
    ui_row_info "Players" "$([ "$(players)" = 2 ] && echo '2 · console + pad' || echo '1 · pad')" "players"
    [ "$STATUS" = "no host" ] || ui_row_action "Refresh games" "refresh"
    [ "$STATUS" = paired ] && ui_row_action "Unpair" "unpair"
    ui_logs_start
    ui_log_add "Moonlight" "$LOGS_PATH/Moonlight.txt"
    ui_row_action "Logs" "logs" "$(ui_logs_count)"
    ui_rows_finish "$LIST"
}
choose_quality() {
    presets | awk -F'|' '{ print $2 "|" $1 }' > "$TEXT.menu"
    q=$(ui_pick "$TEXT.menu" "Quality" "CHOOSE" "$(quality)") || return 0
    echo "$q" > "$QUALITY_FILE"
}
choose_players() {
    printf '%s\n' "# With a Bluetooth pad connected…" \
        "1 · pad${UI_TAB}pad = player 1|1" \
        "2 · console + pad${UI_TAB}pad = player 2|2" \
        "# Without one: the console alone" > "$TEXT.menu"
    n=$(ui_pick "$TEXT.menu" "Players" "CHOOSE" "$(players)") || return 0
    echo "$n" > "$PLAYERS_FILE"
    write_mapping
}
choose_buttons() {
    # Ce qui compte pour un joueur : quel bouton fait « valider / sauter »
    # (A Xbox) dans les jeux PC. labels = mêmes lettres (A, à droite) ;
    # xbox = par position (B, en bas, comme une manette Xbox) ; X/Y suivent.
    printf '%s\n' "# PC confirm / jump is…" \
        "A, right button${UI_TAB}Same letters|labels" \
        "B, bottom button${UI_TAB}Xbox style|xbox" \
        "# X / Y swap the same way" > "$TEXT.menu"
    b=$(ui_pick "$TEXT.menu" "Confirm button" "CHOOSE" "$(buttons)") || return 0
    echo "$b" > "$BUTTONS_FILE"
    write_mapping
}
do_refresh() { ui_run "Connecting to $(host_name)…" refresh; }

# --- points d'entrée de test (stdout, sans brique) --------------------------------------------
case "${1:-}" in
--ml) trap - EXIT INT TERM HUP; shift; ml "$@"; exit $? ;; # appel interne : sans le nettoyage du pak parent
--list-only) refresh >/dev/null; build_list; cat "$LIST"; exit 0 ;;
--status) refresh >/dev/null; echo "status=$STATUS"; exit 0 ;;
--stream-params) stream_params; exit 0 ;;
--scan-tailscale) scan_tailscale; exit 0 ;;
--probe-all) probe_all "$2"; exit 0 ;;
--save-host) save_host "$2" "$3"; cat "$HOSTS"; exit 0 ;;
--current) current_game; apps_ids; exit 0 ;;
--toggle) toggle_app "$2"; exit 0 ;;
--remember-mac) remember_mac "$2"; cat "$MACS" 2>/dev/null; exit 0 ;;
--relocate) relocate_host; cat "$HOST_FILE"; exit 0 ;;
--wake-send) wake_send "$2"; exit 0 ;;
--ip-networks) ip_networks; exit 0 ;;
--option-at) ip_option_at "$2" "$3"; exit 0 ;;
--valid-ip) valid_ip "$2"; exit $? ;;
--try-ip) try_ip "$2"; cat "$HOST_FILE"; exit 0 ;;
esac

paplib_log "Moonlight"
paplib_have minui-list || { ui_message "Moonlight: minui-list is missing." 4; exit 1; }
[ -x "$CLIENT" ] || { ui_message "Moonlight client is missing.\nRedeploy from the Mac." 4; exit 1; }
[ -d "$PORTS_LIB" ] || { ui_message "PortMaster is missing:\nMoonlight needs its libraries." 5; exit 1; }
if [ "${1:-}" = --launch ]; then launch_entry "$2"; exit 0; fi
[ -n "$(host_ip)" ] || choose_host
do_refresh
while :; do
    build_list
    ui_menu "$LIST" "Moonlight" || break
    case "$(ui_action_at)" in
    host) choose_host; do_refresh ;;
    pair) pair; do_refresh ;;
    unpair) unpair; do_refresh ;;
    refresh) do_refresh ;;
    wake) wake_screen; do_refresh ;;
    quality) choose_quality ;;
    buttons) choose_buttons ;;
    players) choose_players ;;
    logs) ui_logs ;;
    toggle:*) n=$(ui_action_at); toggle_app "${n#toggle:}" ;;
    esac
done
