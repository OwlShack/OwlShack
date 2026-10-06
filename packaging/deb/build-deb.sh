#!/usr/bin/env bash
# Package an already-built OwlShack binary as a .deb running under systemd.
#
#   ./build.sh && packaging/deb/build-deb.sh ./OwlShack amd64
#   packaging/deb/build-deb.sh ./OwlShack-linux-arm64 arm64 v1.4.0
#
# Needs dpkg-deb (the Debian host itself needs nothing but systemd).
set -euo pipefail

# Resolved before the cd, so a relative path still means what the caller meant.
BIN="$(realpath "${1:?usage: build-deb.sh <binary> <deb-arch> [version]}")"
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

ARCH="${2:?usage: build-deb.sh <binary> <deb-arch> [version]}"
VERSION="${3:-$(git describe --tags --always 2>/dev/null || echo 0.0.0)}"
VERSION="${VERSION#v}"
# '~' sorts below everything; a literal '-' would read as a Debian revision and sort above.
VERSION="${VERSION//-/\~}"

# armhf also means Pi Zero, whose ARMv6 core faults on a GOARM=7 build. Read the setting the
# toolchain records: it survives the release build's stripped symbols, where a disassembly does not.
if [ "$ARCH" = armhf ]; then
  command -v go >/dev/null || { echo "armhf needs go to read the binary's GOARM" >&2; exit 1; }
  goarm="$(go version -m "$BIN" 2>/dev/null | awk '$1 == "build" && $2 ~ /^GOARM=/ { sub(/^GOARM=/, "", $2); print $2 }')"
  if [ "$goarm" != 6 ]; then
    echo "refusing: $BIN reports GOARM=${goarm:-unknown}, armhf must be GOARM=6 for a Pi Zero" >&2
    exit 1
  fi
fi

STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
# mktemp gives 0700, and dpkg applies the staged root's mode to / on install.
chmod 755 "$STAGE"

install -Dm755 "$BIN" "$STAGE/usr/bin/owlshack"
install -Dm644 packaging/deb/owlshack.service "$STAGE/lib/systemd/system/owlshack.service"
install -Dm644 packaging/deb/default "$STAGE/etc/default/owlshack"
install -Dm644 LICENSE "$STAGE/usr/share/doc/owlshack/copyright"

install -d "$STAGE/DEBIAN"
for script in postinst prerm postrm; do
  install -m755 "packaging/deb/$script" "$STAGE/DEBIAN/$script"
done
echo /etc/default/owlshack > "$STAGE/DEBIAN/conffiles"

cat > "$STAGE/DEBIAN/control" <<CONTROL
Package: owlshack
Version: ${VERSION}
Architecture: ${ARCH}
Maintainer: OwlShack <https://github.com/OwlShack/OwlShack>
Section: net
Priority: optional
Depends: adduser
Installed-Size: $(du -ks "$STAGE" | cut -f1)
Homepage: https://github.com/OwlShack/OwlShack
Description: MeshCore companion, observer and repeater admin
 One static binary that speaks the MeshCore mesh protocol over a USB or SPI
 radio, optionally bridges to MQTT, persists to SQLite and serves its web UI
 on port 8080. Installed as a systemd service running as the owlshack user,
 with its database in /var/lib/owlshack.
CONTROL

OUT="owlshack_${VERSION}_${ARCH}.deb"
dpkg-deb --root-owner-group --build "$STAGE" "$OUT" >/dev/null
echo ">> Done: $OUT ($(du -h "$OUT" | cut -f1))"
