#!/bin/bash
# Build and package the macOS arm64 derivative bundle (Motty.app + DMG) stamped
# with the derivative version from viva.toml + internal/buildinfo VariantSuffix
# (e.g. 0.4.24-bio.1).
set -euo pipefail

here=$(cd "$(dirname "$0")/.." && pwd)
viva_dir=$(cd "$here/../viva" && pwd)
arch=${ARCH:-$(uname -m)}
case "$arch" in
  arm64|aarch64) arch=arm64 ;;
  x86_64|amd64)  arch=amd64 ;;
esac

base_version=$(sed -nE 's/^version[[:space:]]*=[[:space:]]*"([^"]+)"/\1/p' "$here/viva.toml" | head -n 1)
suffix=$(sed -nE 's/^const VariantSuffix = "([^"]*)"/\1/p' "$here/internal/buildinfo/buildinfo.go" | head -n 1)
full_version="${base_version}${suffix}"
if [ -z "$base_version" ]; then
  echo "package_bio_macos: could not read base version from viva.toml" >&2
  exit 1
fi

out_dir=${OUT_DIR:-"$here/build/dist"}
mkdir -p "$out_dir"
work=$(mktemp -d "${TMPDIR:-/tmp}/motty-bio-pkg.XXXXXX")
trap 'rm -rf "$work"' EXIT

echo "== Building viva CLI from $viva_dir"
go -C "$viva_dir" build -o "$work/viva-cli" ./cmd/viva

app_path="$out_dir/Motty.app"
echo "== Packaging Motty.app ($full_version, darwin/$arch)"
rm -rf "$app_path"
(
  cd "$here"
  "$work/viva-cli" package --target darwin --arch "$arch" --format app -o "$app_path"
)

plist="$app_path/Contents/Info.plist"
if [ ! -f "$plist" ]; then
  echo "package_bio_macos: expected $plist" >&2
  exit 1
fi

echo "== Stamping derivative version $full_version into Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $full_version" "$plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleVersion $full_version" "$plist"

sign_identity=${APPLE_SIGNING_IDENTITY:--}
echo "== Signing Motty.app (identity: $sign_identity)"
codesign --force --deep --sign "$sign_identity" \
  --entitlements "$here/assets/macos.entitlements" \
  "$app_path"
codesign --verify --deep --strict "$app_path"

dmg_path="$out_dir/motty-${full_version}-darwin-${arch}.dmg"
rm -f "$dmg_path"
stage="$work/dmg-stage"
mkdir -p "$stage"
cp -R "$app_path" "$stage/Motty.app"
ln -s /Applications "$stage/Applications"

echo "== Creating DMG: $dmg_path"
hdiutil create \
  -volname "Motty ${full_version}" \
  -srcfolder "$stage" \
  -ov \
  -format UDZO \
  "$dmg_path"

echo ""
echo "Done:"
echo "  App: $app_path"
echo "  DMG: $dmg_path"
