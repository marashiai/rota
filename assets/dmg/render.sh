#!/bin/bash
# Renders background.html into background.png and background@2x.png, which dmgbuild joins into one
# Retina background. Run it after changing background.html; it needs Google Chrome.
set -euo pipefail
cd "$(dirname "$0")"
CHROME=${CHROME:-"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}
profile=$(mktemp -d) # a profile of its own: never the one of a Chrome that is open
trap 'rm -rf "$profile"' EXIT
for scale in 1 2; do
  out=background.png; [ "$scale" = 2 ] && out=background@2x.png
  "$CHROME" --headless --user-data-dir="$profile" --hide-scrollbars --force-device-scale-factor="$scale" \
    --window-size=640,440 --screenshot="$PWD/$out" "file://$PWD/background.html" >/dev/null 2>&1
done
sips -g pixelWidth -g pixelHeight background.png background@2x.png | paste - - - | awk '{print $1, $3 "x" $5}'
