#!/usr/bin/env bash
# paks/ci.sh — run by GitHub Actions (.github/workflows/paks.yml).
#
# Each pak with a MANIFEST is released once per version (tag
# pak-<dir>-v<version>, from its pak.json): bump "version" to publish.
# Then the catalogue storefront.json (Pak Store format, plus url and sha256)
# and its signature storefront.json.sig (nextui/sign.sh) are uploaded to the
# "paks" release, where the console's `pak` reads and checks them.
set -euo pipefail
HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
: "${GH_TOKEN:?GH_TOKEN missing}" "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY missing}"
REPO="$GITHUB_REPOSITORY"
DIST="${RUNNER_TEMP:-/tmp}/paks"
rm -rf "$DIST"; mkdir -p "$DIST"
entries="$DIST/entries.jsonl"; : > "$entries"

for m in "$HERE"/*/MANIFEST; do
  d="$(basename "$(dirname "$m")")"
  meta="$HERE/$d/pak.json"
  version="$(jq -r .version "$meta")"
  tag="pak-$d-v$version"
  file="$d.pak.zip"
  if gh release view "$tag" >/dev/null 2>&1; then
    echo "→ $d $version: already released"
  else
    "$HERE/release.sh" "$DIST" "$d"
    gh release create "$tag" "$DIST/$file" --title "$(jq -r .name "$meta") $version" \
      --notes "$(jq -r .name "$meta") $version" --latest=false ${GITHUB_SHA:+--target "$GITHUB_SHA"}
    echo "→ $d $version: released"
  fi
  # Size and checksum as GitHub reports them for the published file.
  asset="$(gh api "repos/$REPO/releases/tags/$tag" --jq ".assets[] | select(.name == \"$file\")")"
  sha="$(jq -r '.digest // ""' <<< "$asset" | sed 's/^sha256://')"
  [ ${#sha} -eq 64 ] || { echo "ERROR: no checksum for $tag/$file" >&2; exit 1; }
  jq -c -n --slurpfile p "$meta" --arg id "h700mods-$d" --arg repo "https://github.com/$REPO" \
    --arg file "$file" --arg url "https://github.com/$REPO/releases/download/$tag/$file" \
    --arg sha "$sha" --argjson size "$(jq .size <<< "$asset")" \
    '$p[0] | {id: $id, name, version, type, description, author, platforms,
      repo_url: $repo, release_filename: $file, url: $url, sha256: $sha, size: $size,
      update_ignore: (.update_ignore // []), categories: (.categories // []),
      scripts: (.scripts // {})}' >> "$entries"
done

jq -s '{name: "NextUI H700 mods", paks: .}' "$entries" > "$DIST/storefront.json"
gh release view paks >/dev/null 2>&1 ||
  gh release create paks --title "Paks catalogue" --latest=false \
    --notes "storefront.json: the catalogue read by the console's pak command."
"$HERE/../nextui/sign.sh" "$DIST/storefront.json"
gh release upload paks "$DIST/storefront.json" "$DIST/storefront.json.sig" --clobber
echo "→ catalogue: $(jq '.paks | length' "$DIST/storefront.json") paks"
