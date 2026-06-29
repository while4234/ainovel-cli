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
| 2026-06-29 | fix: harden import text handling | fix | import prompts, import parser/analyzer/foundation, text encoding utils, global prompt template | `go test ./internal/host/imp ./internal/utils ./internal/globalprompt ./assets ./internal/userrules`; `go build ./cmd/ainovel-cli` passed. | Improves external novel import stability with prompt-side JSON discipline, tolerant JSON extraction, UTF-8 cleanup, UTF-16/GBK decoding tests, and the current customized global prompt template. |
| 2026-06-29 | feat: add global prompt prefix | feat | internal/globalprompt, assets prompt loading, agent/co-create/user-rules prompt wiring | `go test ./internal/globalprompt ./assets ./internal/userrules ./internal/host/... ./internal/agents/...`; `go vet` same scope; `go build ./cmd/ainovel-cli` passed. Full `go test ./...` still fails in bootstrap/notify/version due existing Windows assumptions. | Adds a replaceable global system prompt template and idempotent injection across built-in prompt assets, coordinator/subagents, co-create, user-rules normalizer, and writer summary system prompt. |
| 2026-06-29 | feat: add novel adaptation mode | feat | startup/headless/TUI/host/tools/store/domain/docs/tests | Focused adaptation and entry tests passed; full `go test ./...` still fails in bootstrap/notify/version due existing Windows/config assumptions | Adds source snapshot storage, adaptation planning, writer tools, commit gate, TUI/headless entry, and README docs. |
