#!/usr/bin/env bash
# Assemble a zypper/dnf-compatible RPM repo tree from goreleaser output.
#
# Input:  dist/*.rpm  (produced by `goreleaser release --clean` or `--snapshot`)
# Output: dist/rpm-repo/
#           chatmem.repo         (zypper .repo file — publish under GitHub Pages root)
#           x86_64/<pkg>.rpm     + repodata/
#           aarch64/<pkg>.rpm    + repodata/
#
# `createrepo_c` is not available on macOS Homebrew; we run it inside a
# Fedora container so this script works identically on macOS and Linux.
#
# Usage:
#   scripts/build-rpm-repo.sh [BASE_URL]
#
# BASE_URL defaults to the main chatmem repo's GitHub Pages URL — override
# if you host the repo somewhere else (S3, Cloudflare Pages, your own domain).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$REPO_ROOT/dist"
OUT="$DIST/rpm-repo"
BASE_URL="${1:-https://sid077.github.io/chatmem}"

if ! ls "$DIST"/*.rpm >/dev/null 2>&1; then
  echo "no .rpm files found in $DIST — run 'goreleaser release --snapshot --clean' first" >&2
  exit 1
fi

rm -rf "$OUT"
mkdir -p "$OUT/x86_64" "$OUT/aarch64"

for rpm in "$DIST"/*.x86_64.rpm;  do [ -e "$rpm" ] && cp "$rpm" "$OUT/x86_64/";  done
for rpm in "$DIST"/*.aarch64.rpm; do [ -e "$rpm" ] && cp "$rpm" "$OUT/aarch64/"; done

# Run createrepo_c inside a Fedora container so we don't depend on it being
# installed locally. The container mounts $OUT so the metadata lands next to
# the rpms on the host FS.
docker run --rm -v "$OUT:/repo" fedora:41 bash -c '
  set -e
  dnf install -q -y createrepo_c >/dev/null
  for arch in x86_64 aarch64; do
    createrepo_c --quiet /repo/$arch
  done
' >/dev/null

cat > "$OUT/chatmem.repo" <<REPO
[chatmem]
name=chatmem
baseurl=$BASE_URL/\$basearch/
enabled=1
autorefresh=1
gpgcheck=0
type=rpm-md
REPO

echo "wrote $OUT/"
find "$OUT" -maxdepth 3 -type f | sort
echo
echo "publish the contents of $OUT under $BASE_URL and users can install with:"
echo "  sudo zypper ar $BASE_URL/chatmem.repo"
echo "  sudo zypper --gpg-auto-import-keys refresh && sudo zypper in chatmem"
