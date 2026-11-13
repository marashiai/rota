#!/bin/bash
# Publishes a GitHub release: rota.dmg, the macOS binary, Linux binaries for hosts, and checksums.
#   ./release.sh v0.1.0            # tags HEAD as v0.1.0 if the tag does not exist yet
#   ./release.sh v0.1.0 --draft    # extra flags go to gh release create
# Needs gh logged in with write access to the repository; the repo is taken from the origin remote.
set -euo pipefail
cd "$(dirname "$0")"
VERSION=${1:?usage: release.sh vX.Y.Z [gh release flags]}; shift || true
case "$VERSION" in v[0-9]*) ;; *) echo "version must look like v0.1.0" >&2; exit 2;; esac
[ -z "$(git status --porcelain)" ] || { echo "commit or stash your changes first" >&2; exit 2; }

VERSION="$VERSION" ./build.sh >/dev/null
GOOS=linux  GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-X main.version=$VERSION" -o dist/rota-linux-amd64 .
GOOS=linux  GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-X main.version=$VERSION" -o dist/rota-linux-arm64 .
cp dist/rota.app/Contents/MacOS/rota "dist/rota-darwin-$(uname -m)"
mv dist/rota.dmg "dist/rota-$VERSION.dmg"
(cd dist && shasum -a 256 rota-*.dmg rota-* > SHA256SUMS)

git rev-parse -q --verify "refs/tags/$VERSION" >/dev/null || { git tag -a "$VERSION" -m "$VERSION"; git push -q origin "$VERSION"; }
NOTES="rota $VERSION

- rota-$VERSION.dmg: macOS app (menu bar tray + terminal command), unsigned: right-click, Open the first time
- rota-darwin-$(uname -m): the same binary on its own
- rota-linux-amd64, rota-linux-arm64: for hosts that join with 'rota join' (no tray)
- SHA256SUMS

Install: open the dmg and drag rota to Applications, or copy a binary to ~/.local/bin/rota; then 'rota init'."
# create first, upload after: uploading in the same call failed right after a repository rename
gh release create "$VERSION" --title "rota $VERSION" --notes "$NOTES" "$@"
gh release upload "$VERSION" dist/rota-"$VERSION".dmg dist/rota-* dist/SHA256SUMS --clobber
