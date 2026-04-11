#!/bin/sh
# trawl install script.
#
# Detects OS + arch, downloads the latest trawl release tarball from
# GitHub, extracts the binary to $INSTALL_DIR (defaults to ~/.local/bin),
# and prints next steps. No dependencies beyond curl, tar, and uname.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/jeffdhooton/trawl/main/scripts/install.sh | sh
#
# Customization via env vars:
#   TRAWL_VERSION   pin to a specific tag (default: latest)
#   INSTALL_DIR     where to drop the binary (default: ~/.local/bin)
#   TRAWL_REPO      override the GitHub repo (default: jeffdhooton/trawl)

set -eu

REPO="${TRAWL_REPO:-jeffdhooton/trawl}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${TRAWL_VERSION:-}"

info()  { printf '\033[1;34mtrawl:\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33mtrawl:\033[0m %s\n' "$*" >&2; }
fail()  { printf '\033[1;31mtrawl:\033[0m %s\n' "$*" >&2; exit 1; }

# ---------- detect platform ----------

uname_s=$(uname -s 2>/dev/null || echo unknown)
uname_m=$(uname -m 2>/dev/null || echo unknown)

case "$uname_s" in
  Darwin)  os=darwin ;;
  Linux)   os=linux ;;
  *)       fail "unsupported OS: $uname_s (trawl ships for darwin and linux only)" ;;
esac

case "$uname_m" in
  x86_64|amd64)  arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *)             fail "unsupported arch: $uname_m (trawl ships for amd64 and arm64 only)" ;;
esac

info "detected platform: ${os}_${arch}"

# ---------- resolve version ----------

if [ -z "$VERSION" ]; then
  info "looking up latest release..."
  if ! VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null \
    | grep '"tag_name":' \
    | head -n1 \
    | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/'); then
    fail "failed to query GitHub API for latest release"
  fi
  if [ -z "$VERSION" ]; then
    fail "no 'latest' release found on github.com/${REPO}. Set TRAWL_VERSION to a specific tag (e.g. v0.1.0)."
  fi
fi
info "installing trawl ${VERSION}"

# Strip leading 'v' for the archive name template: trawl_0.1.0_darwin_arm64.tar.gz
version_no_v=${VERSION#v}
archive="trawl_${version_no_v}_${os}_${arch}.tar.gz"
url="https://github.com/${REPO}/releases/download/${VERSION}/${archive}"

# ---------- download + extract ----------

tmpdir=$(mktemp -d 2>/dev/null || mktemp -d -t trawl)
trap 'rm -rf "$tmpdir"' EXIT

info "downloading ${url}"
if ! curl -fsSL -o "${tmpdir}/${archive}" "$url"; then
  fail "download failed. Check that ${VERSION} exists at github.com/${REPO}/releases"
fi

# Fetch and verify the checksum. The tarball naming template is stable
# so the checksum file should always be alongside the archive.
checksum_file="trawl_${version_no_v}_checksums.txt"
checksum_url="https://github.com/${REPO}/releases/download/${VERSION}/${checksum_file}"
if curl -fsSL -o "${tmpdir}/${checksum_file}" "$checksum_url" 2>/dev/null; then
  # Extract the expected hash line for just our archive.
  expected=$(grep " ${archive}$" "${tmpdir}/${checksum_file}" | awk '{print $1}')
  if [ -n "$expected" ]; then
    if command -v shasum >/dev/null 2>&1; then
      got=$(shasum -a 256 "${tmpdir}/${archive}" | awk '{print $1}')
    elif command -v sha256sum >/dev/null 2>&1; then
      got=$(sha256sum "${tmpdir}/${archive}" | awk '{print $1}')
    else
      warn "no shasum or sha256sum on PATH — skipping checksum verification"
      got="$expected"
    fi
    if [ "$got" != "$expected" ]; then
      fail "checksum mismatch! expected=$expected got=$got"
    fi
    info "checksum verified"
  else
    warn "couldn't find ${archive} in checksum file — skipping verification"
  fi
else
  warn "couldn't download checksum file — skipping verification"
fi

info "extracting..."
tar -xzf "${tmpdir}/${archive}" -C "$tmpdir"

if [ ! -f "${tmpdir}/trawl" ]; then
  fail "extracted archive did not contain a 'trawl' binary"
fi

# ---------- install ----------

mkdir -p "$INSTALL_DIR"
install -m 0755 "${tmpdir}/trawl" "${INSTALL_DIR}/trawl"

info "installed to ${INSTALL_DIR}/trawl"

# ---------- PATH advisory ----------

case ":$PATH:" in
  *":${INSTALL_DIR}:"*) : ;;
  *)
    warn "${INSTALL_DIR} is not on your PATH. Add it with:"
    printf '\n  export PATH="%s:$PATH"\n\n' "$INSTALL_DIR" >&2
    warn "(then open a new shell or source your rc file)"
    ;;
esac

# ---------- next steps ----------

cat <<EOF

Next steps:

  1. Verify the install:
       trawl version

  2. Scrape one URL as a smoke test:
       trawl scrape https://example.com --format markdown

  3. Read the docs:
       https://github.com/${REPO}#readme

Full command surface:
  trawl scrape   — scrape one URL
  trawl batch    — scrape a URL list (CSV, TSV, or text)
  trawl crawl    — BFS-crawl a site from a seed URL
  trawl map      — enumerate URLs from a site
  trawl sitemap  — discover URLs from sitemap(s)
  trawl resume   — resume an interrupted job

See 'trawl <command> --help' for the full flag surface.
EOF
