#!/bin/sh
# spm — SP Manager, terminal only (SSH, adb shell, Terminal). No screen UI:
# the pak just carries the binary and passes arguments through unchanged.
PAK_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec "$PAK_DIR/spm" "$@"
