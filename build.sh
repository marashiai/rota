#!/bin/bash
# Builds rota.app and rota.dmg for this Mac. No developer account: the app is unsigned (ad-hoc
# signed so macOS keeps its identity stable). ./build.sh install also puts it in ~/Applications
# and links ~/.local/bin/rota to it, so Spotlight and the terminal run the same binary.
set -euo pipefail
cd "$(dirname "$0")"
APP=dist/rota.app
rm -rf dist && mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources" dist/icon.iconset
go build -ldflags "-X main.version=${VERSION:-dev}" -o "$APP/Contents/MacOS/rota" .
cat > "$APP/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleName</key><string>rota</string>
  <key>CFBundleDisplayName</key><string>rota</string>
  <key>CFBundleIdentifier</key><string>local.rota.team</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>CFBundleShortVersionString</key><string>1.0</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleExecutable</key><string>rota</string>
  <key>CFBundleIconFile</key><string>rota</string>
  <key>LSUIElement</key><true/>
  <key>LSMinimumSystemVersion</key><string>12.0</string>
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
ln -s /Applications dist/Applications
hdiutil create -quiet -volname rota -srcfolder dist -ov -format UDZO dist/rota.dmg
rm dist/Applications
echo "built dist/rota.app and dist/rota.dmg"
if [ "${1:-}" = install ]; then
  DEST=/Applications; [ -w /Applications ] || DEST=~/Applications
  mkdir -p "$DEST" && rm -rf "$DEST/rota.app" && cp -R "$APP" "$DEST/"
  mkdir -p ~/.local/bin && ln -sfn "$DEST/rota.app/Contents/MacOS/rota" ~/.local/bin/rota
  echo "installed $DEST/rota.app; ~/.local/bin/rota -> it. Spotlight: rota"
fi
