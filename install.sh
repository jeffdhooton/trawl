#!/bin/sh
# Local install: build trawl from source, put it on PATH, and register
# the Claude Code skill at ~/.claude/skills/trawl/SKILL.md.
#
# Usage (from the repo root):
#   ./install.sh
#
# What it does:
#   1. go install ./cmd/trawl → $GOPATH/bin/trawl
#   2. Copies skill/SKILL.md → ~/.claude/skills/trawl/SKILL.md
#
# Idempotent — run it again after pulling new changes to refresh both.

set -eu

REPO_ROOT="$(cd "$(dirname "$0")" && pwd)"

info()  { printf '\033[1;34mtrawl:\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33mtrawl:\033[0m %s\n' "$*" >&2; }
fail()  { printf '\033[1;31mtrawl:\033[0m %s\n' "$*" >&2; exit 1; }

# ---------- build + install binary ----------

if ! command -v go >/dev/null 2>&1; then
  fail "go not found on PATH. Install Go first: https://go.dev/dl/"
fi

info "building trawl..."
cd "$REPO_ROOT"
go install ./cmd/trawl

GOBIN="${GOBIN:-$(go env GOPATH)/bin}"
if [ ! -x "${GOBIN}/trawl" ]; then
  fail "go install succeeded but ${GOBIN}/trawl not found"
fi

info "installed binary to ${GOBIN}/trawl"

# PATH advisory
case ":$PATH:" in
  *":${GOBIN}:"*) : ;;
  *)
    warn "${GOBIN} is not on your PATH. Add it with:"
    printf '\n  export PATH="%s:$PATH"\n\n' "$GOBIN" >&2
    ;;
esac

# ---------- install Claude Code skill ----------

SKILL_SRC="${REPO_ROOT}/skill/SKILL.md"
SKILL_DST="$HOME/.claude/skills/trawl/SKILL.md"

if [ ! -f "$SKILL_SRC" ]; then
  fail "skill/SKILL.md not found in repo — something is wrong"
fi

mkdir -p "$(dirname "$SKILL_DST")"

# Remove stale gstack symlink if present
if [ -L "$SKILL_DST" ]; then
  info "removing stale symlink at ${SKILL_DST}"
  rm "$SKILL_DST"
fi

cp "$SKILL_SRC" "$SKILL_DST"
info "installed skill to ${SKILL_DST}"

# ---------- done ----------

cat <<EOF

Done. Verify with:
  trawl version

To pick up the new skill, restart Claude Code.
EOF
