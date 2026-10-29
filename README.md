# NextUI H700 mods

Modifications de [NextUI](https://github.com/LoveRetro/NextUI) pour une
Anbernic RG35XX SP (H700), compilées par GitHub Actions pour chaque release
de NextUI : launcher (cadran d'état, MENU → Tools), menu en jeu, Clock,
Battery, Settings et les briques `minui-list` / `minui-presenter`.

Ce dépôt est **généré** depuis un projet privé (`mac/publish-nextui-mods.sh`) :
ne pas le modifier ici.

| Chemin | Rôle |
| --- | --- |
| `nextui/patches/` | patches appliqués au tag officiel de NextUI |
| `nextui/build.sh` | compilation dans la toolchain officielle (`ghcr.io/loveretro/h700-toolchain`) |
| `nextui/ci.sh` | compile et publie une release par version de NextUI |
| `nextui/patchset.sh` | empreinte des patches (une release à jour n'est pas recompilée) |

Chaque release `NextUI-<date>-<n>` contient `nextui-mods-<build>.tar` (les
binaires, `SOURCE` = tag et commit NextUI compilés, `PATCHSET`) et
`SHA256SUMS`. La console la télécharge elle-même quand cette version de
NextUI sort, vérifie l'empreinte (SHA256SUMS et empreinte annoncée par
GitHub), et la garde la pose au premier démarrage de la nouvelle version.

Compilation : chaque jour toutes les 6 h (nouvelle release de NextUI), à
chaque changement de `nextui/`, ou à la main (Actions → NextUI mods → Run
workflow, avec un tag et « force »).

Licences : patches et binaires suivent celles des projets d'origine (NextUI,
minui-list, minui-presenter).
