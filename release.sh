#!/bin/bash
# Publishes a GitHub release with rota-macos-arm64.dmg, the app for Apple Silicon Macs, and its
# checksum. Intel Macs and Linux build rota from source (see the README).
#   ./release.sh v0.1.0            # tags HEAD as v0.1.0 if the tag does not exist yet
#   ./release.sh v0.1.0 --draft    # extra flags go to gh release create
# Run it on an Apple Silicon Mac, with gh logged in with write access to the repository; the
# repository is the origin remote's, also when it goes through an SSH host alias.
set -euo pipefail
cd "$(dirname "$0")"
VERSION=${1:?usage: release.sh vX.Y.Z [gh release flags]}; shift || true
case "$VERSION" in v[0-9]*) ;; *) echo "version must look like v0.1.0" >&2; exit 2;; esac
[ -z "$(git status --porcelain)" ] || { echo "commit or stash your changes first" >&2; exit 2; }
[ "$(uname -sm)" = "Darwin arm64" ] || { echo "build the release on an Apple Silicon Mac" >&2; exit 2; }
export GH_REPO=${GH_REPO:-$(git remote get-url origin | sed -E 's#\.git$##; s#^.*[:/]([^/]+/[^/]+)$#\1#')}

DMG=rota-macos-arm64.dmg # the same name in every release: .../releases/latest/download/$DMG is the newest
GOOS=darwin GOARCH=arm64 VERSION="$VERSION" ./build.sh >/dev/null
for b in rota gloss; do # Apple Silicon only, and every Mac from macOS 13 on, not only this one's version
  f=dist/rota.app/Contents/MacOS/$b
  [ "$(lipo -archs "$f")" = arm64 ] || { echo "$f is not arm64 only" >&2; exit 1; }
  minos=$(otool -l "$f" | awk '/LC_BUILD_VERSION/{b=1} b&&/minos/{print $2; exit}')
  [ "$minos" = 13.0 ] || { echo "$f needs macOS $minos, not 13.0" >&2; exit 1; }
done
mv dist/rota.dmg "dist/$DMG"
(cd dist && shasum -a 256 "$DMG" > SHA256SUMS)

git rev-parse -q --verify "refs/tags/$VERSION" >/dev/null || { git tag -a "$VERSION" -m "$VERSION"; git push -q origin "$VERSION"; }
NOTES="rota $VERSION for Apple Silicon Macs (M1 or later) with macOS 13 or newer. rota works only with Claude Code.

**Install:** open $DMG and drag rota to Applications, then open rota from Applications. It appears in the menu bar and links the \`rota\` and \`gloss\` commands in \`~/.local/bin\`. Then build your team with one prompt in Claude Code: https://github.com/$GH_REPO#install-with-one-prompt

**The first time:** the app is not signed with an Apple Developer ID, so macOS refuses to open it at first. Open System Settings › Privacy & Security, click Open Anyway next to the message about rota, and confirm. Or run \`xattr -dr com.apple.quarantine /Applications/rota.app\` in a terminal.

**Intel Macs and Linux:** build rota from source, as the README says; the setup prompt does that for you.

SHA256SUMS holds the checksum of $DMG."
# create first, upload after: uploading in the same call failed right after a repository rename
gh release create "$VERSION" --title "rota $VERSION" --notes "$NOTES" "$@"
gh release upload "$VERSION" "dist/$DMG" dist/SHA256SUMS --clobber
