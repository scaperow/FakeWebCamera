#!/usr/bin/env bash
# Build FakeWebCamera.app for macOS.
#
# Usage:
#   scripts/build-app.sh [--output DIR] [--version X.Y.Z]
#
# The bundle is ad-hoc signed so it can run locally without a Developer ID.
# It is intentionally not sandboxed: the app needs to listen on LAN ports,
# join the WS-Discovery multicast group and spawn ffmpeg.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="$ROOT/build"
VERSION="1.0.0"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --output) OUT_DIR="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    -h|--help) sed -n '2,10p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

APP="$OUT_DIR/FakeWebCamera.app"
CONTENTS="$APP/Contents"
MACOS="$CONTENTS/MacOS"
RESOURCES="$CONTENTS/Resources"

echo "==> cleaning $APP"
rm -rf "$APP"
mkdir -p "$MACOS" "$RESOURCES"

echo "==> building binary"
cd "$ROOT"
CGO_ENABLED=1 GOOS=darwin go build \
  -trimpath \
  -ldflags "-s -w -X main.version=$VERSION" \
  -o "$MACOS/FakeWebCamera" \
  ./cmd/fakecam

echo "==> generating icon"
ICONSET="$OUT_DIR/FakeWebCamera.iconset"
rm -rf "$ICONSET"
mkdir -p "$ICONSET"
go run ./scripts/genicon -out "$OUT_DIR/icon-1024.png"
for spec in \
  "16 icon_16x16.png" \
  "32 icon_16x16@2x.png" \
  "32 icon_32x32.png" \
  "64 icon_32x32@2x.png" \
  "128 icon_128x128.png" \
  "256 icon_128x128@2x.png" \
  "256 icon_256x256.png" \
  "512 icon_256x256@2x.png" \
  "512 icon_512x512.png" \
  "1024 icon_512x512@2x.png"; do
  set -- $spec
  sips -z "$1" "$1" "$OUT_DIR/icon-1024.png" --out "$ICONSET/$2" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$RESOURCES/FakeWebCamera.icns"
rm -rf "$ICONSET" "$OUT_DIR/icon-1024.png"

echo "==> writing Info.plist"
cat > "$CONTENTS/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDevelopmentRegion</key><string>zh_CN</string>
  <key>CFBundleDisplayName</key><string>FakeWebCamera</string>
  <key>CFBundleExecutable</key><string>FakeWebCamera</string>
  <key>CFBundleIconFile</key><string>FakeWebCamera</string>
  <key>CFBundleIdentifier</key><string>com.scaperow.fakewebcamera</string>
  <key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
  <key>CFBundleName</key><string>FakeWebCamera</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>${VERSION}</string>
  <key>CFBundleVersion</key><string>${VERSION}</string>
  <key>LSMinimumSystemVersion</key><string>12.0</string>
  <key>LSUIElement</key><true/>
  <key>NSHighResolutionCapable</key><true/>
  <key>NSLocalNetworkUsageDescription</key>
  <string>FakeWebCamera 需要访问本地网络，以便被手机、VLC 和 NVR 自动发现并播放视频。</string>
</dict>
</plist>
PLIST

echo "==> ad-hoc signing"
codesign --force --deep --sign - "$APP"

echo "==> done"
echo "    $APP"
echo
echo "首次启动:"
echo "    open \"$APP\""
echo "如果 macOS 提示“无法验证开发者”，请在 系统设置 > 隐私与安全性 中点击“仍要打开”。"
