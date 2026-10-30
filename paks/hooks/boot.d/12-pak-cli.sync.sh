#!/bin/sh
# 12-pak-cli.sync.sh — commande `pak` disponible partout, Updater et Pak Store masqués.
#
# Robuste : vit hors de Updates.pak dans /.userdata/bin/pak (auto-guérison si
# manquant), avec lien /usr/bin/pak. Une mise à jour de BaseOS réécrit / : le
# lien revient au démarrage suivant. Si Updates.pak est désactivé/absent, le
# binaire copié dans /.userdata/bin reste utilisable.
# Idempotent, rapide (aucun réseau), toujours exit 0.
SD="${SDCARD_PATH:-/mnt/SDCARD}"
PLAT="${PLATFORM:-h700}"

TARGET_DIR="$SD/.userdata/bin"
TARGET="$TARGET_DIR/pak"
SRC=""

mkdir -p "$TARGET_DIR"

# 1) Si déjà copié et exécutable, on s'assure juste du lien /usr/bin
if [ -x "$TARGET" ]; then
  [ "$(readlink -f /usr/bin/pak 2>/dev/null)" = "$(readlink -f "$TARGET" 2>/dev/null)" ] || ln -sf "$TARGET" /usr/bin/pak 2>/dev/null
else
  # 2) Fallback : le trouver dans Updates.pak encore présent (transition)
  for cand in \
    "$SD/Tools/$PLAT/Updates.pak/pak" \
    "$SD/Tools/Updates.pak/pak" \
    "$SD/Roms/TOOLS/Updates.pak/pak"; do
    [ -x "$cand" ] && SRC="$cand" && break
  done
  # 3) Recherche plus large si déplacé
  if [ -z "$SRC" ]; then
    for cand in "$SD"/Tools/*/Updates.pak/pak "$SD"/Tools/*/*.pak/pak; do
      [ -x "$cand" ] && SRC="$cand" && break
    done
  fi
  # 4) Copier vers /.userdata/bin (source de vérité)
  if [ -n "$SRC" ]; then
    cp -f "$SRC" "$TARGET" && chmod 755 "$TARGET"
  fi
  # Lier vers ce qui est utilisable
  if [ -x "$TARGET" ]; then
    ln -sf "$TARGET" /usr/bin/pak 2>/dev/null
  elif [ -x "$SRC" ]; then
    ln -sf "$SRC" /usr/bin/pak 2>/dev/null
  fi
fi

# Masquer Updater / Pak Store comme avant (si pak disponible)
if [ -x /usr/bin/pak ] || [ -x "$TARGET" ]; then
  PAKBIN="/usr/bin/pak"
  [ -x "$PAKBIN" ] || PAKBIN="$TARGET"
  [ -x "$PAKBIN" ] && "$PAKBIN" hide Updater "Pak Store" >/dev/null 2>&1
fi

exit 0
