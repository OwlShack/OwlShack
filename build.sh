#!/usr/bin/env bash
# Build the OwlShack binary: compile the React SPA, then build the static Go
# binary that embeds it. Mirrors .github/workflows/release.yml.
#
#   ./build.sh                       # -> ./OwlShack, version from git
#   OUTPUT=dist/owlshack ./build.sh  # custom output path
#   VERSION=v1.2.3 ./build.sh        # pin the stamped version
#   FORCE_INSTALL=1 ./build.sh       # reinstall node_modules even if in sync
set -euo pipefail

# Always run from the repo root, regardless of the caller's cwd.
cd "$(dirname "${BASH_SOURCE[0]}")"

OUTPUT="${OUTPUT:-OwlShack}"
# Stamped into the binary (buildinfo.Version, shown in the UI). Defaults to a
# git description, falling back to "dev" outside a checkout.
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"

echo ">> Building frontend"
pushd web/frontend >/dev/null
# npm ci is lockfile-exact + reproducible (matches CI) but wipes and reinstalls
# node_modules every time, which dominates a local rebuild. Skip it when the
# tree is already in sync with the lockfile; FORCE_INSTALL=1 reinstalls anyway.
if [ -n "${FORCE_INSTALL:-}" ] || [ ! -d node_modules ] || [ package-lock.json -nt node_modules ]; then
  npm ci
else
  echo ">> node_modules up to date with package-lock.json, skipping npm ci"
fi
npm run build
popd >/dev/null

# Stamp the build version into the embedded SW so its bytes change every release:
# that's what triggers the browser's SW update (and the reload toast) and rotates
# the cache name. App freshness is already handled by hashed /assets/ URLs.
sed -i "s/__BUILD_VERSION__/${VERSION}/" web/frontend/dist/sw.js

# No spaces: the linker splits -ldflags on whitespace.
# Mirrors the firmware's own build.sh, local clock included, so a ver reply reads the same beside a real node.
BUILD_DATE="$(date '+%d-%b-%Y')"
echo ">> Building backend -> ${OUTPUT} (version ${VERSION}, built ${BUILD_DATE})"
go mod download
CGO_ENABLED=0 go build \
  -trimpath \
  -ldflags "-s -w -X github.com/OwlShack/OwlShack/internal/buildinfo.Version=${VERSION} -X github.com/OwlShack/OwlShack/internal/buildinfo.Date=${BUILD_DATE}" \
  -o "${OUTPUT}" .

echo ">> Done: ${OUTPUT} ($(du -h "${OUTPUT}" | cut -f1))"
