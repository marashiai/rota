#!/bin/bash
# Builds rota.app for this Mac. The app holds rota and gloss, the command the agents write review
# notes with. No developer account: the app is unsigned (ad-hoc signed so macOS keeps its identity
# stable).
#   ./build.sh            dist/rota.app
#   ./build.sh install    also puts it in /Applications (or ~/Applications) and links ~/.local/bin/rota
#                         and gloss to it, so Spotlight and the terminal run the same binaries
#   ./build.sh dmg        also dist/rota.dmg: one window where rota is dragged to Applications.
#                         It needs Python 3.10 or newer, for dmgbuild (assets/dmg)
set -euo pipefail
cd "$(dirname "$0")"
APP=dist/rota.app
V=${VERSION:-dev}
SHORT=${V#v}; [[ $SHORT =~ ^[0-9]+(\.[0-9]+)*$ ]] || SHORT=0.0.0 # what Finder shows, such as 0.1.0
# The oldest macOS Go supports. Without it, cgo would require this Mac's own macOS version of every
# Mac that runs the app. The flags are part of Go's build cache key, so no stale object slips in.
export MACOSX_DEPLOYMENT_TARGET=13.0
export CGO_CFLAGS="${CGO_CFLAGS:--O2 -g} -mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET"
export CGO_LDFLAGS="${CGO_LDFLAGS:-} -mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET"
rm -rf "$APP" dist/rota.dmg dist/icon.iconset # dist/dmgbuild stays: it is only installed once
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources" dist/icon.iconset
go build -ldflags "-X main.version=$V" -o "$APP/Contents/MacOS/rota" .
go build -o "$APP/Contents/MacOS/gloss" ./gloss/cmd/gloss
cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleName</key><string>rota</string>
  <key>CFBundleDisplayName</key><string>rota</string>
  <key>CFBundleIdentifier</key><string>local.rota.team</string>
  <key>CFBundleVersion</key><string>$SHORT</string>
  <key>CFBundleShortVersionString</key><string>$SHORT</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleExecutable</key><string>rota</string>
  <key>CFBundleIconFile</key><string>rota</string>
  <key>LSUIElement</key><true/>
  <key>LSMinimumSystemVersion</key><string>$MACOSX_DEPLOYMENT_TARGET</string>
  <key>NSHighResolutionCapable</key><true/>
</dict></plist>
PLIST
"$APP/Contents/MacOS/rota" icon dist/icon.iconset/icon_512x512@2x.png
for s in 16 32 128 256 512; do
  sips -z $s $s dist/icon.iconset/icon_512x512@2x.png --out "dist/icon.iconset/icon_${s}x${s}.png" >/dev/null
  sips -z $((s*2)) $((s*2)) dist/icon.iconset/icon_512x512@2x.png --out "dist/icon.iconset/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns dist/icon.iconset -o "$APP/Contents/Resources/rota.icns"
rm -rf dist/icon.iconset
codesign --force --deep -s - "$APP" 2>/dev/null || true
echo "built $APP"
case "${1:-}" in
install)
  DEST=/Applications; [ -w /Applications ] || DEST=~/Applications
  mkdir -p "$DEST" && rm -rf "$DEST/rota.app" && cp -R "$APP" "$DEST/"
  mkdir -p ~/.local/bin && ln -sfn "$DEST/rota.app/Contents/MacOS/rota" ~/.local/bin/rota
  G=~/.local/bin/gloss # a gloss of their own stays: only a missing one, or a rota.app's, is linked
  if { [ ! -e "$G" ] && [ ! -L "$G" ]; } || [[ "$(readlink "$G")" == */rota.app/Contents/MacOS/gloss ]]; then
    ln -sfn "$DEST/rota.app/Contents/MacOS/gloss" "$G"
    echo "installed $DEST/rota.app; ~/.local/bin/rota and gloss -> it. Spotlight: rota"
  else
    echo "installed $DEST/rota.app; ~/.local/bin/rota -> it; your own ~/.local/bin/gloss stays. Spotlight: rota"
  fi
  ;;
dmg)
  python3 -c 'import sys; sys.exit(sys.version_info < (3, 10))' ||
    { echo "the disk image needs Python 3.10 or newer, for dmgbuild: brew install python" >&2; exit 2; }
  if ! dist/dmgbuild/bin/python -c 'import dmgbuild' 2>/dev/null; then # missing, or its Python is gone
    rm -rf dist/dmgbuild && python3 -m venv dist/dmgbuild
    dist/dmgbuild/bin/pip install -q --disable-pip-version-check --require-hashes -r assets/dmg/requirements.txt
  fi
  # its output, hdiutil's warnings that macOS deprecates commands among it, shows only if it fails
  dist/dmgbuild/bin/dmgbuild -s assets/dmg/settings.py -D app="$APP" rota dist/rota.dmg >dist/dmgbuild.log 2>&1 ||
    { cat dist/dmgbuild.log >&2; exit 1; }
  echo "built dist/rota.dmg"
  ;;
esac
