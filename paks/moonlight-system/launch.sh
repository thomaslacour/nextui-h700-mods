#!/bin/sh
# MOONLIGHT — système « Moonlight » du menu principal. Chaque entrée de
# Roms/Moonlight (MOONLIGHT)/ (<appli>.ml, écrite par le pak Tools)
# lance l'appli de l'hôte qu'elle nomme. Toute la logique (hôte, stream,
# réglages, visibilité) reste dans Tools/<plateforme>/Moonlight.pak.
SDCARD_PATH="${SDCARD_PATH:-/mnt/SDCARD}"
PLATFORM="${PLATFORM:-h700}"
exec sh "$SDCARD_PATH/Tools/$PLATFORM/Moonlight.pak/launch.sh" --launch "$1"
