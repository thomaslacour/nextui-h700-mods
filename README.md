# NextUI H700 mods

Modifications to [NextUI](https://github.com/LoveRetro/NextUI) for an
Anbernic RG35XX SP (H700), built by GitHub Actions for every NextUI release:
launcher (status dial, MENU → Tools), in-game menu, Clock, Battery, Settings,
and the `minui-list` / `minui-presenter` bricks.

It also releases our NextUI paks (`paks/`, see [paks/README.md](paks/README.md)).

This repository is **generated** from a private project
(`mac/publish-nextui-mods.sh`): do not edit it here.

| Path | Role |
| --- | --- |
| `nextui/patches/` | patches applied to the official NextUI tag |
| `nextui/build.sh` | build in the official toolchain (`ghcr.io/loveretro/h700-toolchain`) |
| `nextui/ci.sh` | builds and publishes one release per NextUI version |
| `nextui/patchset.sh` | patch hash (an up-to-date release is not rebuilt) |
| `paks/` | paks released to the console's `pak` command |

Each `NextUI-<date>-<n>` release holds `nextui-mods-<build>.tar` (the
binaries, `SOURCE` = NextUI tag and commit built, `PATCHSET`) and
`SHA256SUMS`. The console downloads it by itself when that NextUI version
comes out, checks the checksum (SHA256SUMS and the checksum GitHub reports),
and its guard installs it on the first boot of the new version.

Builds run every 6 hours (new NextUI release), on every change to `nextui/`,
or by hand (Actions → NextUI mods → Run workflow, with a tag and "force").

Licenses: patches and binaries follow those of the original projects (NextUI,
minui-list, minui-presenter).
