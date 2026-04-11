# Releasing trawl

This is the operational checklist for cutting a new trawl release. The
infrastructure is all in place — tag a commit, push the tag, and the
GitHub Actions release workflow produces a draft release you can
eyeball and publish.

See also:

- `.goreleaser.yaml` — the build matrix, ldflags, archive template,
  changelog filters. Read it before you change the release pipeline.
- `.github/workflows/release.yml` — the CI workflow that runs
  goreleaser. Reads the Go version from `go.mod` so bumps flow
  through.
- `scripts/install.sh` — the one-liner installer end users copy-paste.
  It pulls the latest published (non-draft) release from the GitHub
  API and honors `TRAWL_VERSION` / `TRAWL_REPO` / `INSTALL_DIR` env
  vars.

## Versioning

trawl uses semver tags (`vMAJOR.MINOR.PATCH`). The informal rule:

- **Patch (v0.1.0 → v0.1.1)**: bug fixes, doc updates, non-breaking
  internal changes.
- **Minor (v0.1.0 → v0.2.0)**: new features, new commands, new
  engines, new schema features, anything a user would notice but
  that doesn't break existing job configs or on-disk formats.
- **Major (v0.x → v1.0.0)**: breaking changes to the CLI surface,
  the JSONL record shape, the frontier BadgerDB schema, the tier
  cache shape, or the schema v1 format. Breaking changes to the
  schema file format are the most likely cause of a major bump —
  see `docs/ROADMAP.md`'s "v2 schema features" bucket.

**Pre-1.0 caveat:** we're free to break things within `v0.x` — that's
the whole point of 0-series versions. But document the break in the
release notes, and if it affects on-disk formats (frontier, tier-
learning cache, content cache) note the migration path or lack
thereof.

## The full checklist

### 1. Pre-flight on a clean working tree

```bash
cd ~/workspace/trawl
git status                         # working tree must be clean
git log --oneline origin/main..    # anything unpushed?
```

If there's uncommitted work, commit or stash it first. If local
main is ahead of `origin/main`, push it — GoReleaser reads
`origin/main` state to build the changelog.

### 2. Run the full test suite locally

```bash
go build ./...
go test -count=1 -short ./...
```

The release workflow reruns these before GoReleaser kicks in, so a
local failure here would block the release on CI anyway. Catching
it locally saves a minute.

**About `-short`:** trawl's chromium tests gate on `chromiumAvailable()`
and skip if Chrome isn't installed, but they're slow on a GitHub
Actions runner (real browser boot). The release workflow runs
`go test -short ./...` to keep release wall time under two minutes.
If you're about to cut a release that touches the chromium engine,
run `go test ./...` (no `-short`) locally first to catch anything
chromium-specific before it bites a user.

### 3. Sanity-check the binary you're about to ship

```bash
go build -o /tmp/trawl-preflight ./cmd/trawl
/tmp/trawl-preflight version
/tmp/trawl-preflight scrape https://example.com --format markdown | head -20
rm /tmp/trawl-preflight
```

If `scrape` hangs, errors out, or produces an empty body on a known-
good URL, something regressed and you should NOT cut a release yet.

### 4. Decide on the version

```bash
# What's the current release?
gh release list --limit 5

# What commits have landed since?
git log --oneline $(git describe --tags --abbrev=0 2>/dev/null || git rev-list --max-parents=0 HEAD)..HEAD
```

Pick `vX.Y.Z` by the semver rules above. No `v0.0.0`, no leading `v`
missing, no pre-release suffix unless you actually want a draft
pre-release (GoReleaser's `prerelease: auto` flag detects `-rc`,
`-beta`, etc. and marks them).

### 5. Tag and push

```bash
git tag v0.2.0
git push origin v0.2.0
```

That's it — no annotated tag needed unless you want a tag message.
GoReleaser uses the commit log for its changelog filter.

### 6. Watch the release workflow

```bash
# Immediately after pushing the tag:
gh run list --workflow=release.yml --limit 1

# Grab the run ID from the output and watch it:
gh run watch <run-id> --exit-status
```

Expected runtime: **~2 minutes**. The steps are:

1. `Check out source` (~5s)
2. `Set up Go` (~30s — cached on subsequent runs)
3. `Verify build and tests pass before releasing` (~20s for the
   short-mode suite)
4. `Run GoReleaser` (~1m — cross-compiles 4 binaries, tars them up,
   uploads to GitHub)

If any step fails, GoReleaser leaves no draft release and you can
re-run the workflow after fixing. Tags are sticky — `git tag -d v0.2.0
&& git push --delete origin v0.2.0` if you need to move the tag,
though that's destructive and should be a last resort.

### 7. Eyeball the draft release

```bash
gh release view v0.2.0 --web
```

Open it in a browser. Check:

- **Changelog** — auto-generated from git log. The filters in
  `.goreleaser.yaml` strip `docs:`, `chore:`, `test:`, `ci:`, `wip:`,
  and typo fixes. If the list looks messy, edit the release body
  manually before publishing.
- **Artifacts** — there should be exactly 5 files:
  - `trawl_X.Y.Z_darwin_amd64.tar.gz`
  - `trawl_X.Y.Z_darwin_arm64.tar.gz`
  - `trawl_X.Y.Z_linux_amd64.tar.gz`
  - `trawl_X.Y.Z_linux_arm64.tar.gz`
  - `trawl_X.Y.Z_checksums.txt`
  Missing platforms mean a build broke silently — check the workflow
  logs.
- **Release body header** — the install one-liner block is templated
  from `.goreleaser.yaml`'s `release.header`. The one-liner URL must
  point at `main/scripts/install.sh` (not the tag) so users always
  get the latest installer even for old tag lookups.

### 8. Publish the draft

**Via web UI** (preferred for releases where you want to eyeball
release notes on a big screen): click "Publish release" at the bottom
of the draft page.

**Via CLI** (fast path for patch releases):

```bash
gh release edit v0.2.0 --draft=false
```

Publishing is what makes the release appear in `gh release list` and
what makes the GitHub API's `releases/latest` endpoint return it.
Until you publish, the install script will say "no published releases
found."

### 9. Smoke-test the install script against the published release

This is the single highest-value post-release check. It verifies the
entire distribution pipeline — GitHub API → tarball download →
checksum verification → extraction → ldflags version injection — not
just the local build.

```bash
rm -rf /tmp/trawl-smoke
INSTALL_DIR=/tmp/trawl-smoke sh scripts/install.sh
/tmp/trawl-smoke/trawl version                    # prints trawl vX.Y.Z (not "dev")
/tmp/trawl-smoke/trawl scrape https://example.com --format markdown | head -5
```

If the `version` output says `dev`, the ldflags injection broke in
the release pipeline — the binary in the tarball is a plain `go build`
with no version. If the `scrape` call errors out on a trivial URL,
something about the distribution is broken even though the workflow
succeeded. Either case: `gh release edit vX.Y.Z --draft=true` to
unpublish while you fix it.

### 10. Announce (optional)

When trawl has more than one power user: update the project README if
anything user-facing changed, mention the release in a changelog
section, post a note wherever users live. Currently that means
updating `docs/ROADMAP.md`'s "current phase" header if the release
crosses a phase boundary.

## Common gotchas

**"go test fails on CI but passes locally"** — the release workflow
uses the Go version declared in `go.mod`. If you bumped `go.mod` but
haven't actually installed that Go version locally, you may be running
tests against an older compiler. `go version` and compare.

**"GoReleaser fails with 'archive not found'"** — usually a
`.goreleaser.yaml` typo in the `builds.binary` or `archives.ids`
keys. Run `goreleaser check` locally to validate the config before
pushing a tag.

**"workflow succeeded but no release appeared"** — check the workflow
log. If it says "release skipped: prerelease auto-detected from tag"
and you didn't mean it to be a prerelease, your tag had a `-rc` /
`-beta` / `-alpha` suffix. Retag.

**"install.sh downloads the tarball but fails SHA256 verification"** —
the checksum file didn't match. Usually a GoReleaser version drift.
Re-run the workflow from the same tag; if that fails, delete the tag
and re-push.

**"`trawl version` says `dev` after installing from the release"** —
the ldflags injection broke. The goreleaser config targets
`github.com/jeffdhooton/trawl/internal/version.{Version,Commit,Date}`;
if you renamed the version package or moved the vars, update the
`builds.ldflags` block in `.goreleaser.yaml`.

**"Chromium tests hang the release workflow"** — the workflow uses
`go test -short ./...` specifically so chromium tests skip. If
someone adds a test that doesn't honor `testing.Short()`, it'll run
on the CI runner where Chrome may or may not be installed and
behave unpredictably. Gate all new chromium tests on
`chromiumAvailable() && !testing.Short()`.

**"Node.js 20 deprecation warnings in the workflow output"** —
GitHub Actions is migrating runners to Node 24 by September 2026.
When the `actions/checkout@v4` and `goreleaser/goreleaser-action@v6`
actions publish Node 24 compatible versions, bump them in
`release.yml`. Current versions still work.

## Cutting a release with local-only GoReleaser (fallback)

If GitHub Actions is down and you need a release out the door:

```bash
# Install goreleaser locally (one-time)
brew install goreleaser

# Tag locally, don't push yet
git tag v0.2.0

# Run the release with a local GitHub token (needs `repo` scope)
export GITHUB_TOKEN=ghp_...
goreleaser release --clean

# Verify, then push the tag so the release is tied to the right commit
git push origin v0.2.0
```

This should be a last resort — the CI workflow exists so every
release is reproducible from a clean Ubuntu runner, not from whatever
state your laptop happens to be in.

## Changing the release matrix

If you want to add Windows support, additional architectures, or
Homebrew/NPM/Docker distribution:

1. Edit `.goreleaser.yaml`. GoReleaser docs are excellent:
   https://goreleaser.com/customization/
2. Validate: `goreleaser check`
3. Test without publishing: `goreleaser release --snapshot --clean`
   (produces a `dist/` directory without creating a GitHub release)
4. Commit the change, tag a new release, watch the workflow.

Windows specifically is non-trivial for trawl — BadgerDB uses
mmap semantics that work fine on Windows but `$TRAWL_HOME` defaults
and path handling are Unix-first. Don't ship Windows without testing
the resume flow, the tier-cache directory, and the content cache on a
real Windows runner.

Don't add Homebrew or similar without bumping to at least `v0.1.x`
where `x ≥ 1`, so there's something to upgrade from.
