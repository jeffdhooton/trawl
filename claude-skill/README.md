# trawl — Claude Code skill

This directory vendors the trawl Claude Code skill so it evolves in
lockstep with the CLI. The canonical `SKILL.md` lives next to this
README.

## Installing

Claude Code reads user-level skills from `~/.claude/skills/<name>/SKILL.md`.
To install trawl's skill:

```sh
mkdir -p ~/.claude/skills/trawl
cp claude-skill/SKILL.md ~/.claude/skills/trawl/SKILL.md
```

Restart Claude Code (or start a new session) — the skill will appear
in `/help` under its documented trigger words ("scrape", "crawl",
"extract pages", "map a site", etc.).

## What the skill does

The skill teaches Claude Code when to route a user's scraping request
to trawl vs. other tools (`WebFetch`, `/browse`, etc.), and documents
the full command surface — flags, output shape, composability with
`jq` and Unix pipes. It's a reference document Claude Code loads into
context when a relevant request shows up, not an executable.

No other Claude Code tools or MCP servers are required beyond the
ones in the skill's `allowed-tools:` frontmatter.

## Staying in sync with the CLI

The skill's `version:` field in its frontmatter tracks the trawl CLI
version it was last updated against. When a release adds new flags,
commands, or output fields, bump `version:` and update the relevant
sections. Treat the skill like documentation — it's allowed to lag a
patch release, but a minor release should include skill updates for
any new user-visible capability.

## Upstream (optional)

If you fork trawl or want the skill under a different name, edit the
`name:` field in SKILL.md's frontmatter and use your preferred path
under `~/.claude/skills/`. Only the file location and `name:` field
matter to Claude Code's loader.
