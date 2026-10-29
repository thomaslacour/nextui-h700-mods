#!/usr/bin/env bash
# preview.sh — rend l'écran de log-viewer dans un PNG, sans console ni matériel.
#
# Usage : ./preview.sh [--file <fichier>] [--title <titre>] [sortie.png]
#         ./preview.sh                                  # exemple intégré
#
# C'est le même rendu que sur la console (640x480, police embarquée) : il sert à
# vérifier la mise en page à gauche et la taille des lignes sans allumer la SP.
set -euo pipefail

HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
OUT=""
ARGS=()
while [ $# -gt 0 ]; do
    case "$1" in
    --file | --title) ARGS+=("$1" "${2:-}"); shift 2 ;;
    *) OUT="$1"; shift ;;
    esac
done
if [ ${#ARGS[@]} -eq 0 ]; then
    SAMPLE="$(mktemp -t logview-sample.XXXXXX)"
    printf '2026-10-19 19:00:00  INFO  celeste : OK IN_SYNC\n2026-10-19 19:00:01  WARN  voidstranger : LOCAL_SAVE_FILES_EMPTY\n' > "$SAMPLE"
    ARGS=(--file "$SAMPLE" --title "Exemple")
fi
OUT="${OUT:-$HERE/preview.png}"

cd "$HERE"
go run . "${ARGS[@]}" --preview "$OUT"
echo "PNG -> $OUT"

# Ouverture automatique sur macOS uniquement.
case "$(uname -s)" in
Darwin) command -v open >/dev/null 2>&1 && open "$OUT" ;;
esac
