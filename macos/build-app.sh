#!/bin/sh
# Assembles HomenetBridge.app: the menu bar app, with the homenet-bridge CLI inside it.
# Usage: macos/build-app.sh <homenet-bridge darwin binary> <version> <output dir>
# UNIVERSAL=1 builds for arm64 and x86_64 (needs Xcode, not only the command line tools).
set -eu

cli=$1
version=$2
out=$3
root=$(cd "$(dirname "$0")/.." && pwd)

archs=
if [ "${UNIVERSAL:-}" = 1 ]; then
	archs="--arch arm64 --arch x86_64"
fi
# shellcheck disable=SC2086 # archs is a list of flags
(cd "$root/macos" && swift build -c release $archs)
# shellcheck disable=SC2086
bin=$(cd "$root/macos" && swift build -c release $archs --show-bin-path)/HomenetBridge

app=$out/HomenetBridge.app
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$bin" "$app/Contents/MacOS/HomenetBridge"
cp "$cli" "$app/Contents/MacOS/homenet-bridge"

# Icon from the rendered PNGs (assets/icon/png).
iconset=$(mktemp -d)/AppIcon.iconset
mkdir -p "$iconset"
png=$root/assets/icon/png
cp "$png/icon-16.png" "$iconset/icon_16x16.png"
cp "$png/icon-32.png" "$iconset/icon_16x16@2x.png"
cp "$png/icon-32.png" "$iconset/icon_32x32.png"
cp "$png/icon-128.png" "$iconset/icon_128x128.png"
sips -z 64 64 "$png/icon-128.png" --out "$iconset/icon_32x32@2x.png" >/dev/null
sips -z 256 256 "$png/icon-512.png" --out "$iconset/icon_128x128@2x.png" >/dev/null
sips -z 256 256 "$png/icon-512.png" --out "$iconset/icon_256x256.png" >/dev/null
cp "$png/icon-512.png" "$iconset/icon_256x256@2x.png"
cp "$png/icon-512.png" "$iconset/icon_512x512.png"
iconutil -c icns "$iconset" -o "$app/Contents/Resources/AppIcon.icns"

cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key><string>io.github.frigori.homenet-bridge</string>
	<key>CFBundleName</key><string>homenet-bridge</string>
	<key>CFBundleDisplayName</key><string>homenet-bridge</string>
	<key>CFBundleExecutable</key><string>HomenetBridge</string>
	<key>CFBundleIconFile</key><string>AppIcon</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>$version</string>
	<key>CFBundleVersion</key><string>$version</string>
	<key>LSMinimumSystemVersion</key><string>13.0</string>
	<!-- Menu bar only: no Dock icon, no app menu. -->
	<key>LSUIElement</key><true/>
	<key>NSHumanReadableCopyright</key><string>MIT License</string>
</dict>
</plist>
PLIST

# Ad-hoc signature: enough for notifications; not notarized.
codesign --force --sign - "$app/Contents/MacOS/homenet-bridge"
codesign --force --sign - "$app"
echo "$app"
