# Paks

NextUI paks for the RG35XX SP (H700), installed and upgraded on the console
by the `pak` command and the Updates pak.

| Pak | What it does |
| --- | --- |
| Music | Subsonic / Navidrome player: albums, artists, playlists, search; keeps playing after exit |

## How a pak is released

- `paks/<dir>/MANIFEST` lists the files of the pak: `<path in the pak> <source>`,
  sources relative to `paks/`, `go:<dir>` for a Go program (Linux ARM64).
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
