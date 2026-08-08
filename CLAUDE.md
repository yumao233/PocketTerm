# WebShell — Claude Code Project Instructions

The development conventions for this project are maintained centrally in `AGENTS.md` (a single file shared by Claude Code / Codex / Cursor).

@AGENTS.md

## Reading Priority

1. First read `AGENTS.md` **§1 Architecture and Layering**, **§8 PTY/WS/SSH Core Domain**, **§13 Frontend Code Conventions**, and **§14 Forbidden Items List** — these are hard constraints where "violation causes bugs".
2. Before changing HTTP interfaces/fields, you must read **§11 Frontend Contract Sync Checklist**.
3. Before committing, you must read the verification trio in **§2 Development Commands** (`go build` + `go test` + `npm test`).

## Relationship With Global Preferences

Personal global preferences (communication style: reply in Chinese while preserving English technical terms; coding: high cohesion, low coupling) live in `~/.claude/CLAUDE.md`; this file does not repeat them and only adds project-specific conventions.