#!/bin/sh
# post-install.sh — run by `pak` after installing or updating Moonlight.pak
# (from the pak folder): the "Moonlight" system of the main menu, a small
# launcher in Emus/<platform>/MOONLIGHT.pak (pak.json's post_uninstall removes it).
set -e
dst="${SDCARD_PATH:-/mnt/SDCARD}/Emus/${PLATFORM:-h700}/MOONLIGHT.pak"
mkdir -p "$dst"
cp system/launch.sh "$dst/launch.sh.new"
chmod 755 "$dst/launch.sh.new"
mv "$dst/launch.sh.new" "$dst/launch.sh"
