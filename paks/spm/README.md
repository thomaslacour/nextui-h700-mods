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
spm search <titre> [--refresh]
spm info <id> [--refresh]
spm update [--refresh]
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

## ScreenScraper et cache

Identifiants dans `SS_DEVID`, `SS_DEVPASSWORD`, `SS_ID`, `SS_PASSWORD`, ou
`.userdata/h700/spm/screenscraper.json`. Chaque variable d'environnement
présente prévaut sur le fichier. Ne pas publier ces valeurs ; les permissions
sur une carte FAT/exFAT ne les protègent pas réellement.

Le cache ne contient que la requête sans authentification et sa réponse de
métadonnées. `SPM_CACHE_DIR` change son emplacement ; `SPM_CACHE_TTL` définit
sa fraîcheur (défaut `24h`). Limite : **64 entrées / 16 Mio** de fichiers
publiés, plus un temporaire d'écriture borné à environ 4 Mio. Les entrées les
plus anciennes sont évincées ; les fichiers JSON étrangers et les liens sont
ignorés par le nettoyage. Écriture atomique, sans stocker les identifiants.

- Cache frais utilisable sans réseau ni identifiants chargés.
- `update` rafraîchit les entrées périmées ; `update --refresh` les force toutes.
- `search/info --refresh` force la requête correspondante.
- Délai HTTP de 20 s, redirections refusées, erreurs sans URL authentifiée.

Les appels ScreenScraper sont testés avec un serveur HTTP simulé, **pas encore
avec un compte réel**. Le lecteur SQLite maison ne traite pas les données du
WAL : l'audit les signale, l'historique peut être incomplet tant qu'il est actif.

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
