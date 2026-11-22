#!/bin/bash
# Cross-build and package the Windows portable zip (motty.exe + bundled ConPTY
# + motty-data) stamped with the derivative version from viva.toml +
# internal/buildinfo VariantSuffix (e.g. 0.4.24-bio.2).
set -euo pipefail

here=$(cd "$(dirname "$0")/.." && pwd)
viva_dir=$(cd "$here/../viva" && pwd)
arch=${ARCH:-amd64}
case "$arch" in
  amd64|x86_64|x64) arch=amd64 ;;
  arm64|aarch64)    arch=arm64 ;;
  *) echo "package_bio_windows: unsupported arch: $arch" >&2; exit 2 ;;
esac

base_version=$(sed -nE 's/^version[[:space:]]*=[[:space:]]*"([^"]+)"/\1/p' "$here/viva.toml" | head -n 1)
suffix=$(sed -nE 's/^const VariantSuffix = "([^"]*)"/\1/p' "$here/internal/buildinfo/buildinfo.go" | head -n 1)
full_version="${base_version}${suffix}"
if [ -z "$base_version" ]; then
  echo "package_bio_windows: could not read base version from viva.toml" >&2
  exit 1
fi

out_dir=${OUT_DIR:-"$here/build/dist"}
mkdir -p "$out_dir"
work=$(mktemp -d "${TMPDIR:-/tmp}/motty-bio-win.XXXXXX")
trap 'rm -rf "$work"' EXIT

echo "== Building viva CLI from $viva_dir"
go -C "$viva_dir" build -o "$work/viva-cli" ./cmd/viva

program="$work/motty.exe"
echo "== Cross-building motty.exe ($full_version, windows/$arch)"
(
  cd "$here"
  "$work/viva-cli" build --target windows --arch "$arch" -o "$program"
)

commit_time=$(git -C "$here" show -s --format=%cI HEAD 2>/dev/null || date -u +"%Y-%m-%dT%H:%M:%SZ")
declare -a portable=()
if [ "$arch" = "amd64" ]; then
  portable=(
    -file "internal/localshell/conpty/LICENSE.txt=conpty/LICENSE.txt"
    -file "internal/localshell/conpty/x64/arm64/OpenConsole.exe=conpty/x64/arm64/OpenConsole.exe"
    -file "internal/localshell/conpty/x64/conpty.dll=conpty/x64/conpty.dll"
    -file "internal/localshell/conpty/x64/x64/OpenConsole.exe=conpty/x64/x64/OpenConsole.exe"
  )
else
  portable=(
    -file "internal/localshell/conpty/LICENSE.txt=conpty/LICENSE.txt"
    -file "internal/localshell/conpty/arm64/arm64/OpenConsole.exe=conpty/arm64/arm64/OpenConsole.exe"
    -file "internal/localshell/conpty/arm64/conpty.dll=conpty/arm64/conpty.dll"
  )
fi

zip_path="$out_dir/motty-${full_version}-windows-${arch}.zip"
rm -f "$zip_path"
echo "== Packing portable zip: $zip_path"
(
  cd "$here"
  go run ./tools/portablezip -program "$program" -time "$commit_time" -o "$zip_path" "${portable[@]}"
)

unzip -l "$zip_path" > "$work/zip-list.txt"
if ! grep -q ' conpty/' "$work/zip-list.txt"; then
  echo "package_bio_windows: portable zip missing conpty files" >&2
  exit 1
fi

echo ""
echo "Done:"
echo "  Windows portable zip: $zip_path"
