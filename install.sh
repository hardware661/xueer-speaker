#!/bin/sh
# Installs the xueer-speaker program from the latest GitHub release:
#   curl -fsSL https://raw.githubusercontent.com/hardware661/xueer-speaker/master/install.sh | sh
# Puts it in ~/.local/bin (or $XS_BIN_DIR). Linux and macOS; on Windows
# download xueer-speaker-windows-amd64.exe from the Releases page.
set -eu
repo=hardware661/xueer-speaker
dir=${XS_BIN_DIR:-$HOME/.local/bin}
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in linux|darwin) ;; *) echo "不支持的系统：$os（Windows 请到 Releases 页下载 .exe）" >&2; exit 1 ;; esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "不支持的 CPU：$(uname -m)" >&2; exit 1 ;;
esac
name=xueer-speaker-$os-$arch
base=https://github.com/$repo/releases/latest/download
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
echo "下载 $name …"
curl -fsSL -o "$tmp/$name" "$base/$name"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"
want=$(grep " $name\$" "$tmp/SHA256SUMS" | cut -d' ' -f1)
if command -v sha256sum >/dev/null; then got=$(sha256sum "$tmp/$name" | cut -d' ' -f1); else got=$(shasum -a 256 "$tmp/$name" | cut -d' ' -f1); fi
[ -n "$want" ] && [ "$want" = "$got" ] || { echo "校验不对，没装" >&2; exit 1; }
mkdir -p "$dir"
install -m 755 "$tmp/$name" "$dir/xueer-speaker"
echo "装好了：$dir/xueer-speaker"
case ":$PATH:" in *":$dir:"*) ;; *) echo "把 $dir 加进 PATH，或者直接用完整路径运行。" ;; esac
echo "下一步：xueer-speaker login"
