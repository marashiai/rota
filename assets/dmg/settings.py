# dmgbuild settings for rota's disk image: one window with rota on the left, an arrow, and
# Applications on the right, so installing is one drag. ./build.sh dmg runs dmgbuild with them from
# the repository's root, with the app as -D app=dist/rota.app.
import os.path

app = defines["app"]  # noqa: F821: dmgbuild gives settings their -D values as defines
name = os.path.basename(app)

format = "ULMO"  # LZMA: the smallest download, and every macOS rota runs on opens it
files = [app]
symlinks = {"Applications": "/Applications"}
# Finder shows an app without ".app" by itself. hide_extensions would also say so, but it puts
# Finder information on rota.app, which a strict check of its signature refuses.
icon =os.path.join(app, "Contents", "Resources", "rota.icns")  # the disk's own icon
background = "assets/dmg/background.png"  # with background@2x.png beside it: one Retina image

# The window's height counts its title bar: 400 points of background show below one of 28. The
# background is 40 points taller, so a taller title bar crops its foot and a shorter one shows more
# of it, never a strip of plain window.
window_rect = ((200, 140), (640, 428))
icon_size = 128
text_size = 14
icon_locations = {name: (160, 192), "Applications": (480, 192)}
