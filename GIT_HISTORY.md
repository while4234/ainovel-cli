# Git History

## Repository Policy

- Local Git is used for rollback, review, and bug investigation.
- Secret files such as `.env`, keys, and credential files are intentionally ignored.
- Remote pushes require explicit user instruction or a project-specific opt-in.

## Quick Commands

- Status: `git status --short`
- Recent history: `git log --oneline --decorate -n 20`
- Inspect a commit: `git show <hash>`
- Restore a file from a commit: `git restore --source <hash> -- <path>`

## Change Log

| Date | Commit message | Type | Files | Validation | Notes |
| --- | --- | --- | --- | --- | --- |
| 2026-06-29 | feat: add novel adaptation mode | feat | startup/headless/TUI/host/tools/store/domain/docs/tests | Focused adaptation and entry tests passed; full `go test ./...` still fails in bootstrap/notify/version due existing Windows/config assumptions | Adds source snapshot storage, adaptation planning, writer tools, commit gate, TUI/headless entry, and README docs. |
