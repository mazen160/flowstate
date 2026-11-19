#!/usr/bin/env bash
# Unpack the dev packages the Linux desktop UI (Fyne/GLFW) links against,
# without root. Files land in .deps/ and the Makefile picks them up.
#
# Use this when `sudo apt-get install` isn't available. If you do have root,
# prefer installing the packages system-wide instead — see the README.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
dest="$root/.deps"
pkgs=(libwayland-dev libffi-dev libxxf86vm-dev)

for tool in apt-get dpkg-deb; do
	command -v "$tool" >/dev/null || { echo "needs $tool (Debian/Ubuntu only)" >&2; exit 1; }
done

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$dest"

echo "→ downloading ${pkgs[*]}"
(cd "$tmp" && apt-get download "${pkgs[@]}" >/dev/null)
for deb in "$tmp"/*.deb; do
	dpkg-deb -x "$deb" "$dest"
done

# The dev packages ship bare `libfoo.so` symlinks pointing at `libfoo.so.N`
# runtime files that live in the system libdir. Copy those in so the linker
# can resolve -lwayland-client etc. against this prefix. Pin to the native
# multiarch triplet: a bare `*-linux-gnu` glob also matches i386, and a
# 32-bit .so makes the linker bail with "skipping incompatible".
multiarch=$(gcc -dumpmachine 2>/dev/null || echo x86_64-linux-gnu)
libdir="$dest/usr/lib/$multiarch"
if [ -d "$libdir" ]; then
	for pattern in libwayland-client libwayland-cursor libwayland-egl libwayland-server libXxf86vm; do
		for f in /usr/lib/"$multiarch"/"$pattern".so.*; do
			[ -e "$f" ] && cp --update=none "$f" "$libdir/" || true
		done
	done
fi

echo "✓ UI dev files unpacked into .deps/ — \`make build\` will now find them"
