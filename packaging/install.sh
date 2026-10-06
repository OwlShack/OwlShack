#!/bin/sh
# One-shot installer for Debian, Ubuntu and Raspberry Pi OS: picks the .deb for
# this machine, installs it, and leaves OwlShack running under systemd.
#
#   curl -fsSL https://raw.githubusercontent.com/OwlShack/OwlShack/dev/packaging/install.sh | sudo sh
#
#   URL=./owlshack.deb   install a package you already have (a local build works)
set -eu

REPO=OwlShack/OwlShack

[ "$(id -u)" = 0 ] || { echo "Run this as root: pipe it to 'sudo sh', or sudo sh install.sh" >&2; exit 1; }
command -v dpkg >/dev/null && command -v apt-get >/dev/null || { echo "Not a Debian-based system. Use the release binary or Docker: https://github.com/$REPO#install" >&2; exit 1; }
command -v curl >/dev/null || { echo "curl is required: apt install curl" >&2; exit 1; }

# dpkg's own answer: a 64-bit OS on 32-bit-capable hardware still gets it right.
ARCH="$(dpkg --print-architecture)"
case "$ARCH" in
  amd64|arm64|armhf|i386) ;;
  *) echo "No .deb for $ARCH. Release binaries cover it: https://github.com/$REPO/releases" >&2; exit 1 ;;
esac

URL="${URL:-}"
if [ -z "$URL" ]; then
  # Newest-first, and unlike /latest it includes pre-releases.
  API="https://api.github.com/repos/$REPO/releases"
  URL="$(curl -fsSL "$API" | grep -o "https://[^\"]*owlshack_[^\"]*_${ARCH}\.deb" | head -1)"
  [ -n "$URL" ] || { echo "No ${ARCH} package found. See https://github.com/$REPO/releases" >&2; exit 1; }
fi

DEB="$(mktemp -t owlshack-XXXXXX.deb)"
trap 'rm -f "$DEB"' EXIT
echo ">> Fetching $URL"
curl -fsSL -o "$DEB" "$URL"

echo ">> Installing"
# apt, not dpkg -i: it resolves the adduser dependency.
DEBIAN_FRONTEND=noninteractive apt-get install -y "$DEB"

# The service's own log is the only witness to the port it bound.
ADDR=""
i=0
[ -d /run/systemd/system ] || i=15   # no systemd, no service, nothing to wait for
while [ -z "$ADDR" ] && [ "$i" -lt 15 ]; do
  ADDR="$(journalctl -u owlshack -n 200 --no-pager 2>/dev/null | sed -n 's/.*web UI listening addr=\([^ ]*\).*/\1/p' | tail -1)"
  [ -n "$ADDR" ] || { i=$((i + 1)); sleep 1; }
done
IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
echo
case "$ADDR" in
  :*) echo ">> Installed. OwlShack is at http://${IP:-localhost}${ADDR}" ;;
  ?*) echo ">> Installed. OwlShack is at http://${ADDR}" ;;
  *)  echo ">> Installed, but the service has not logged a listen address yet." ;;
esac
echo "   systemctl status owlshack        service state"
# sudo, not bare: reading the journal needs root or membership of adm/systemd-journal, and an
# unprivileged systemctl status drops the log lines from its output without saying so.
echo "   sudo journalctl -u owlshack -f   logs"
echo "   /etc/default/owlshack            PORT, HOST, TZ, extra flags"
