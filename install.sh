#!/bin/sh
# Install homenet-bridge from GitHub Releases, checking the download against checksums.txt.
#
#   curl -fsSL https://raw.githubusercontent.com/frigori/homenet-bridge/main/install.sh | sh
#
# Environment:
#   HOMENET_BRIDGE_VERSION      version to install, e.g. 0.1.3 (default: latest release)
#   HOMENET_BRIDGE_INSTALL_DIR  where to put the binary (default: /usr/local/bin, using sudo
#                               if needed; ~/.local/bin when that is not possible)
set -eu

REPO=frigori/homenet-bridge
NAME=homenet-bridge

say() { printf '%s\n' "$*" >&2; }
die() { say "error: $*"; exit 1; }

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL -o "$2" "$1"; }
	latest_url() { curl -fsSLI -o /dev/null -w '%{url_effective}' "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -q -O "$2" "$1"; }
	latest_url() { wget -q -S --spider "$1" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1 | tr -d '\r'; }
else
	die "curl or wget is required"
fi

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) die "unsupported OS $(uname -s); on Windows download the zip from https://github.com/$REPO/releases" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) die "unsupported architecture $(uname -m) (releases cover amd64 and arm64)" ;;
esac

version=${HOMENET_BRIDGE_VERSION:-}
if [ -z "$version" ]; then
	# /releases/latest redirects to /releases/tag/vX.Y.Z; no API token or rate limit involved.
	version=$(latest_url "https://github.com/$REPO/releases/latest" | sed -n 's#.*/tag/v##p')
	[ -n "$version" ] || die "could not find the latest release"
fi
version=${version#v}

archive="${NAME}_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/v$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading $NAME $version for $os/$arch..."
fetch "$base/$archive" "$tmp/$archive" || die "download failed: $base/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "download failed: $base/checksums.txt"

expected=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$expected" ] || die "$archive is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
	actual=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
else
	die "sha256sum or shasum is required to verify the download"
fi
[ "$actual" = "$expected" ] || die "checksum mismatch for $archive (expected $expected, got $actual)"
say "Checksum OK."

tar -xzf "$tmp/$archive" -C "$tmp" "$NAME"

dir=${HOMENET_BRIDGE_INSTALL_DIR:-}
sudo=
if [ -z "$dir" ]; then
	dir=/usr/local/bin
	if [ ! -w "$dir" ] && [ "$(id -u)" -ne 0 ]; then
		if command -v sudo >/dev/null 2>&1; then
			sudo=sudo
			say "Installing to $dir needs sudo."
		else
			dir="$HOME/.local/bin"
		fi
	fi
fi
$sudo mkdir -p "$dir"
$sudo install -m 0755 "$tmp/$NAME" "$dir/$NAME"
say "Installed $dir/$NAME ($("$dir/$NAME" version))."

case ":$PATH:" in
*":$dir:"*) ;;
*) say "Note: $dir is not on your PATH." ;;
esac
if [ "$os" = linux ]; then
	say "If this machine runs a firewall (ufw, firewalld), allow 8096/tcp and 7359/udp from the LAN while sharing."
fi
