# Paks

NextUI paks for the RG35XX SP (H700), installed and upgraded on the console
by the `pak` command and the Updates pak.

| Pak | What it does |
| --- | --- |
| Gamepad | the console as a Bluetooth game pad for a Mac, PC or phone |
| Moonlight | stream games from a PC (Apollo / Sunshine); adds a "Moonlight" system to the main menu |
| Music | Subsonic / Navidrome player: albums, artists, playlists, search; keeps playing after exit |
| Pad Setup | map an external pad the SDL database gets wrong |

## How a pak is released

- `paks/<dir>/MANIFEST` lists the files of the pak: `<path in the pak> <source>`
  from the repo root; `go:<dir>` builds a Go program (Linux ARM64), a trailing
  `/` copies a folder, `@run <command>` runs a build step first (C code and the
  Moonlight client are built in the NextUI toolchain container).
- `pak.json` may carry `scripts` (`post_install`, `post_update`,
  `post_uninstall`), run by `pak` from the pak folder.
- Bumping `"version"` in `paks/<dir>/pak.json` releases it: the **Paks**
  workflow builds `<dir>.pak.zip` (`release.sh`) and publishes it as release
  `pak-<dir>-v<version>`.
- The catalogue `storefront.json` (Pak Store format, plus `url`, `sha256`,
  `size`) is attached to the `paks` release.

## On the console

```sh
echo https://github.com/thomaslacour/nextui-h700-mods/releases/download/paks/storefront.json \
  >> /mnt/SDCARD/.userdata/h700/pak/sources
pak update && pak upgrade
```

`pak` checks the SHA-256 of every archive and refuses one without it. Settings
kept in `.userdata/` are never touched; files listed in `update_ignore` are
kept on upgrade.

```sh
paks/release.sh /tmp/out music   # build a zip locally to inspect it
```

Moonlight bundles moonlight-embedded (GPL-3.0) from the PortMaster port
"Moonlight New"; sources and licenses: `moonlight/build.sh`, `client/licenses/`.
