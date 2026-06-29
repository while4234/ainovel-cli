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
| 2026-06-29 | fix: log adaptation preparation failures | fix | TUI adaptation preparation | `go test ./internal/entry/tui` passed using `C:\Program Files\Go\bin\go.exe`. | Writes complete source-analysis errors to `output/novel/logs/tui.log` so provider HTTP failures are not lost behind the modal width. |
| 2026-06-29 | fix: tolerate UTF-8 BOM in config files | fix | bootstrap config file loader/tests | `go test ./internal/bootstrap` passed using `C:\Program Files\Go\bin\go.exe`. | Strips UTF-8 BOM before JSON comment removal/unmarshal so Windows-written config files no longer break startup with `invalid character 'ï'`. |
| 2026-06-29 | fix: persist model switch candidates | fix | bootstrap config/setup, host model switching, bootstrap tests | `go test ./internal/bootstrap`; `go test ./internal/entry/tui`; `go test ./internal/host` passed using `C:\Program Files\Go\bin\go.exe`. | Keeps previously selected provider/model pairs in `providers.<name>.models` so switching from DeepSeek to GPT does not make DeepSeek disappear from `/model`. |
| 2026-06-29 | feat: select global prompt by model family | feat | internal/globalprompt, bootstrap model creation, `/model` UI, assets docs, bootstrap config tests | `go test ./internal/entry/tui ./internal/globalprompt ./internal/bootstrap ./assets`; `go test ./internal/globalprompt ./internal/bootstrap ./assets ./internal/agents ./internal/userrules ./internal/host/imp ./internal/host/sim ./internal/host/adapt` passed; `go build -o ainovel-cli.exe ./cmd/ainovel-cli` passed. | Splits the embedded global prompt into DeepSeek and GPT templates, injects the selected template at the LLM model boundary so `/model` switches follow the active model, shows option counts in `/model`, and fixes Windows test home isolation. |
| 2026-06-29 | feat: show adaptation preparation progress | feat | TUI adaptation startup modal, adaptation store cleanup, host startup, tests | `go test ./internal/host/adapt ./internal/entry/tui ./internal/host` passed; `go test ./internal/store -run TestAdaptationStore` and `go test ./internal/host/adapt -run TestPrepareRunWorksAfterResetGenerated` passed. Full `go test ./internal/store` hit existing Windows `t.TempDir` cleanup failures (`directory is not empty`) in unrelated tests. | Shows chapter-level source analysis progress before adaptation co-create and preserves source snapshots when starting confirmed adaptation runs. |
| 2026-06-29 | fix: avoid default chapter length for external novels | fix | user rules defaults, import/adaptation startup paths, tests | `go test ./internal/rules ./internal/userrules`; `go test ./internal/entry/tui ./internal/entry/headless ./internal/host` passed. Full `go test ./...` still fails in unrelated bootstrap/notify/tools/version Windows/environment cases. | Keeps anti-AI mechanical rules for imported/adapted novels while omitting the original-writing 3000-6000 default chapter length unless explicitly set by the user. |
| 2026-06-29 | fix: harden import text handling | fix | import prompts, import parser/analyzer/foundation, text encoding utils, global prompt template | `go test ./internal/host/imp ./internal/utils ./internal/globalprompt ./assets ./internal/userrules`; `go build ./cmd/ainovel-cli` passed. | Improves external novel import stability with prompt-side JSON discipline, tolerant JSON extraction, UTF-8 cleanup, UTF-16/GBK decoding tests, and the current customized global prompt template. |
| 2026-06-29 | feat: add global prompt prefix | feat | internal/globalprompt, assets prompt loading, agent/co-create/user-rules prompt wiring | `go test ./internal/globalprompt ./assets ./internal/userrules ./internal/host/... ./internal/agents/...`; `go vet` same scope; `go build ./cmd/ainovel-cli` passed. Full `go test ./...` still fails in bootstrap/notify/version due existing Windows assumptions. | Adds a replaceable global system prompt template and idempotent injection across built-in prompt assets, coordinator/subagents, co-create, user-rules normalizer, and writer summary system prompt. |
| 2026-06-29 | feat: add novel adaptation mode | feat | startup/headless/TUI/host/tools/store/domain/docs/tests | Focused adaptation and entry tests passed; full `go test ./...` still fails in bootstrap/notify/version due existing Windows/config assumptions | Adds source snapshot storage, adaptation planning, writer tools, commit gate, TUI/headless entry, and README docs. |
