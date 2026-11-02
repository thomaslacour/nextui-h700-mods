# spm — SP Manager

Un seul binaire Go, terminal uniquement (SSH, adb shell, Terminal). Aucun
service ni écran NextUI. Version locale en développement, pas encore publiée
ou testée sur la console.

## Commandes

```text
spm list [--json]
spm audit [--json]                  # alias doctor
spm history list [--json]
spm history search <mots…>
spm history audit
spm clean                          # plan uniquement
spm fix names                      # plan uniquement
spm search <mots…> [--all|-a]        # NAME/SYSTEM ; détails avec --all
spm info <id|nom|chemin>
spm update                         # relire les sources locales
spm cache status
spm cache clean [--all]
spm backup --to <dossier> [--dry-run | --apply] [--mount <volume>] [--json]
spm bk --to <dossier> …             # alias backup
spm restore --from <backup> [--dry-run | --apply] [--mount <volume>] [--json]
spm verify --from <backup> [--mount <volume>] [--json]
```

`SDCARD_PATH` désigne la carte (défaut `/mnt/SDCARD`). Les commandes `clean` et
`fix names` refusent toujours l'application : leurs plans ne sont pas encore
des opérations de nettoyage/renommage disponibles.

## Backup et restauration

Créer **un nouveau dossier vide sur la destination voulue**, après avoir
monté le support. Ne pas réutiliser un ancien snapshot. Exemples pour une
clé déjà montée sur `/media/usb` et son dossier vide `spm-snapshot` :

```sh
spm bk --to /media/usb/spm-snapshot --mount /media/usb -n
spm bk --to /media/usb/spm-snapshot --mount /media/usb --apply
spm verify --from /media/usb/spm-snapshot --mount /media/usb
spm restore --from /media/usb/spm-snapshot --mount /media/usb -n
spm restore --from /media/usb/spm-snapshot --mount /media/usb --apply
```

- Sans `--apply` : lecture et vérification seulement, aucun fichier créé.
  `-n` et `--dry-run` sont synonymes. Les combiner avec `--apply` est une erreur.
  `--yes` ne déclenche pas une écriture ; cette option n'est pas acceptée ici.
- La destination doit exister et être hors de la carte SD, y compris après
  résolution des alias de montage/liens du chemin racine. `--mount` (ou
  `SPM_BACKUP_MOUNT`) exige un volume réellement monté : aucun dossier de
  remplacement n'est créé si la clé est absente. Sans cette option, seul le
  dossier existant choisi est contrôlé ; sa présence ne prouve pas un montage.
- Copie des fichiers reconnus sous `Saves/` : `.sav`, `.srm`, `.rtc`, `.state`,
  `.state<n>`, `.state.auto`, `.st0`…`.st9`. Les états sous
  `.userdata/shared/<TAG>-<core>/` sont inclus. Les AppleDouble, fichiers de
  configuration, logs, ROMs et sauvegardes natives PortMaster ne sont pas inclus.
  Les cartes PS1 partagées `.mcd` ne sont pas réattribuées/restaurées automatiquement.
- Lecture/copie par flux, empreintes SHA-256 et tailles vérifiées, manifest
  publié en dernier. Un inventaire changeant ou une lecture impossible bloque
  le succès : pas de snapshot déclaré complet silencieusement.
- Une restauration vérifie **tout le backup avant d'écrire**, refuse les chemins
  traversants, liens symboliques, doublons (y compris casse FAT/exFAT), fichiers
  hors des emplacements de saves et checksums invalides. Elle vérifie aussi
  que les fichiers courants n'ont pas changé avant leur remplacement.
- Les fichiers à remplacer sont conservés et vérifiés dans
  `<backup>/.spm-restore-backup/<identifiant unique>/`, avec manifest et journal
  `restore-progress.json`. Le chemin est affiché. Un rollback non vide peut
  lui-même être vérifié puis restauré avec les mêmes commandes.
- Les jeux détectés en cours d'exécution bloquent l'application. La restauration
  prend aussi le verrou de transfert des sauvegardes du dashboard. Ne pas
  jouer, synchroniser ni modifier les fichiers pendant l'opération.
- Les deux supports sont contrôlés pour l'espace libre, avec réserve de
  256 Mio. Un espace libre inconnu est une erreur, pas une autorisation.

**Ce n'est pas une transaction atomique sur toute la carte.** Après une coupure
ou une erreur en cours de restauration, certains fichiers peuvent déjà être
restaurés ; conserver le journal et les originaux, examiner la situation avant
de retirer un verrou ou de réessayer. Aucune récupération destructive automatique.
Les backups ne sont **pas chiffrés** : choisir un support de confiance.

## Recherche hors ligne et enrichissement facultatif

`search` et `info` parcourent directement `Roms/`, avec les mêmes règles que
NextUI. Aucun XML, compte ou réseau n'est nécessaire. Tous les mots recherchés
doivent apparaître dans le nom, fichier, système ou les métadonnées facultatives.

```sh
spm search zelda
spm search zelda boy
spm info 'Roms/Game Boy (GB)/Zelda.gb'
spm update
```

`search` affiche NAME et SYSTEM par défaut ; `--all` / `-a` ajoute ID et PATH.
Les IDs affichés sont les chemins relatifs des ROMs, stables et distincts entre
systèmes. `info` accepte aussi un nom de fichier ou un titre non ambigu.
Les fichiers `gamelist.xml` à la racine ou sous `Roms/<système>/` peuvent enrichir
les jeux existants (titre, description, éditeur, genre, etc.) par chemin exact.
Les références XML vers des ROMs absentes n'ajoutent pas de faux résultats.
Un XML invalide émet un avertissement sans masquer les ROMs.

`update` relit les sources locales et compte les ROMs : **aucun téléchargement ni
index persistant**. Chaque recherche relit les fichiers ; `--refresh` n'est donc
pas nécessaire. `cache status|clean` reste disponible pour les anciens fichiers
de cache générés par spm, sans effacer les fichiers étrangers ou les XML sources.

Pour obtenir de vraies descriptions, **Skyscraper** (à distinguer de Skraper)
peut utiliser un compte utilisateur ScreenScraper sans demander à l'utilisateur
ses propres identifiants développeur. Il produit des gamelists EmulationStation.
Voir [les sources supportées](https://gemba.github.io/skyscraper/SCRAPINGMODULES/)
et [l'installation](https://gemba.github.io/skyscraper/INSTALLATION/).
Des AppImages Linux ARM64 sont proposées, mais leur fonctionnement sur NextUI
n'a pas été validé : ce n'est pas un binaire Go statique léger. Préférer un
ordinateur ou l'OrangePi pour le scraping, avec cache et sortie hors de la SD.
Tester sur une copie de quelques ROMs, conserver les noms et produire les XML
dans un dossier séparé avant toute copie sur la console. Ne jamais écraser des
XML/images existants sans sauvegarde. Les chemins XML doivent correspondre aux
ROMs de la console (idéalement relatifs).

Le lecteur SQLite maison ne traite pas les données du WAL : l'audit les signale,
l'historique peut être incomplet tant qu'il est actif.

## Tests et compilation

```sh
cd paks/spm/spm
gofmt -l .
go vet ./...
go test -race ./...
mkdir -p ../../../build
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o ../../../build/spm-linux-arm64 .
```

Depuis la racine : `sh tests/spm.sh` (cartes temporaires uniquement).
