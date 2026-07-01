# Git Notes

## Repository Purpose

This repository contains the `ainovel-cli` Go command line application and its
supporting assets, documentation, scripts, and tests.

## Ignore And Secret Policy

Local executables, generated build/release artifacts, workspaces, output
directories, the repo-local `novel/` runtime/user-data directory, editor files,
and local `.ainovel/` configuration are ignored. Never commit provider
credentials, API keys, `.env` values, private keys, cookies, or raw local auth
paths. Safe example configuration files may be tracked when they contain
placeholders only.

## Project GitHub Rule

For this project, a completed future development task is not done until the
intentional changes are committed and pushed to the configured GitHub remote
(`origin`) unless the user explicitly says not to push for that task.

## Project Operating Preference

For this project, after each completed generation or development task that
changes the running project, backend, or Web UI, rebuild/restart the local
`ainovel-cli.exe web` service and explicitly tell the user to refresh the Web
page. The normal local Web URL is `http://127.0.0.1:9898`.

## Current Baseline

- Latest source change: branch `codex/web-project-cocreate-controls`,
  `feat: add word budget progress UI`.
- Branch: `codex/web-project-cocreate-controls`
- Remote: `origin` -> `https://github.com/while4234/ainovel-cli.git`
- Working tree: expected clean after the Web project/co-create controls commit,
  except for ignored local `.codex/`, `.playwright-mcp/`, and output/runtime
  artifacts.
- Runtime: local `D:\ainovel\ainovel-cli.exe` is rebuilt after the Web
  project/co-create controls work using the project-local Go 1.25.5 toolchain,
  then restarted on
  `http://127.0.0.1:9898` with runtime root
  `C:\Users\RondleLiu\.ainovel\novels-preview`. The `D:\grok\bin\ainovel-cli.cmd`
  shim still points to `D:\ainovel\ainovel-cli.exe`.
- Validation: `go test ./internal/domain ./internal/entry/startup ./internal/entry/web ./internal/tools ./internal/store ./internal/userrules ./cmd/ainovel-cli`,
  `npm --prefix web test`, `npm --prefix internal/entry/web/ui test`,
  `npm --prefix web run build`, `npm --prefix internal/entry/web/ui run build`,
  `go build -o D:\ainovel\ainovel-cli.exe D:\ainovel\cmd\ainovel-cli`,
  `git diff --check`, HTTP smoke checks for `/`, `/api/runtime`, and
  `/api/projects` on `http://127.0.0.1:9898`, and Playwright desktop/narrow UI
  checks succeeded on 2026-07-01. The latest runtime is on 9898 and 9904 has no
  listener.

## Change Log

- 2026-07-01 `feat: add word budget progress UI`: added workspace progress
  display, normal co-create total-word controls, sticky right-side co-create
  launch/planning area, overflow/mobile layout fixes, total-vs-per-chapter
  user-rule wording, regenerated embedded Vite assets, rebuilt the local binary,
  and restarted only port 9898.
- 2026-07-01 `6549c2a` `Implement backend word budget contract`: added the
  persisted `WordBudget` contract, quick/co-create payload handling, startup
  prompt injection, `novel_context` runtime budget payload, outline-derived
  chapter budgets, and non-adaptation commit gating.
- 2026-07-01 `feat: improve web project and cocreate controls`: added Web
  project rename and internal-trash APIs, ChatGPT-style project context menu
  UI, active-session cleanup on trash, one-click equal-width co-create
  suggestion buttons, editable pre-commit co-create user messages with revise
  regeneration, backend/frontend tests, and regenerated embedded Vite assets.
- 2026-07-01 `9d40f9b` `feat: add web grok oauth login`: added Web Grok OAuth
  login start/poll/complete/status APIs, surfaced the flow in the model add
  form, registered final Grok providers via `auth:"grok_oauth"` and
  `account_id`, added backend/frontend tests, and regenerated embedded Vite
  assets.
- 2026-07-01 `2017558` `feat: close web tui operation gaps`: added Web APIs
  and UI controls for fresh quick start, pause/abort, external novel import,
  export, diagnostics, and generic model add; aligned Web adaptation/co-create
  behavior with TUI and regenerated embedded Vite assets.
- 2026-07-01 `6f38d2a` `style: refresh web workbench UI`: refreshed the React
  Web UI with a Windows 11 Fluent-inspired light workbench, compact right
  inspector tabs, fixed-height command bar, responsive narrow layout, and
  regenerated embedded Vite assets.
- 2026-07-01 `96a384b` `docs: finalize web packaging and smoke coverage`:
  added Web UI static embed and HTTP smoke coverage, CLI default parser
  regression, README Web runtime docs, `runtime_root` config example comments,
  and `/novel/` ignore protection.
- 2026-07-01 `860d487` `feat: add web model admin status pages`: added project
  model routing, usage/cache reporting, backend status/testing APIs, UI tabs,
  and project-overlay persistence fixes accepted by re-review.
- 2026-07-01 `c084600` `feat: add web co-create workflow`: added normal,
  stage, and adapt co-create flows with suggestion chips, stream state, and
  ready draft launch/restore behavior accepted by re-review.
- 2026-07-01 `66b4539` `feat: add web adaptation workflow`: added source upload,
  preparation analysis, constrained `chapter` / `arc` / `free` mode selection,
  rewrite policy mapping, and adaptation start UI/API accepted by re-review.
- 2026-07-01 `d315035` `feat: add web simulation profile flow`: added project-scoped
  simulate file upload, analysis trigger, JSON profile import, upload safety
  checks, UI panel, and embedded assets accepted by review.
- 2026-07-01 `7331eda` `feat: add web session workbench`: added project
  session management, snapshot/resume/continue/steer/events APIs, SSE history
  replay, embedded React/Vite workbench, and concurrency/error-propagation
  fixes accepted by re-review.
- 2026-07-01 `4c5d7e8` `feat: add web runtime entrypoint`: added
  `ainovel-cli web`, `internal/entry/web`, runtime-root and project manifest
  tests, and `Host.SimulateFromDir`; independent review passed and mechanical
  validation succeeded before commit.
- 2026-07-01 `202b21b` `docs: clarify adaptation mode selection`: pulled
  latest upstream code, installed project-local Go 1.25.5 under ignored
  `.codex/tools/`, rebuilt `D:\ainovel\ainovel-cli.exe`, and verified the
  PowerShell/cmd `ainovel-cli` entrypoints show commit `202b21b`.
- 2026-06-30 `fix: bound TUI frame height`: top-level TUI rendering
  now pads/truncates every view to the active terminal size, with a done-state
  regression test covering stale multi-line input height so completed/export
  prompts cannot keep scrolling as repeated old frames.
- 2026-06-30 `docs: record global CLI sync`: rebuild
  `D:\ainovel\ainovel-cli.exe` from `7bafcc6`, add `D:\ainovel` to the current
  user's PATH, install `D:\grok\bin\ainovel-cli.cmd` for already-opened PATH
  environments, and verify `ainovel-cli --version` from outside the repo in
  both PowerShell and cmd.
- 2026-06-30 `7bafcc6` `fix: scope adaptation writer guidance`: pulled latest
  upstream code from GitHub and rebuilt the local runtime executable.
- 2026-06-30 `1dd5d60` `fix: stabilize retries and TUI input rendering`: shared
  retry policy now uses 7 attempts with increasing delay for co-create,
  structured source analysis, user-rule normalization, and subagent calls. TUI
  co-create failures now show an actionable retry/start prompt, and global input
  rendering is clamped to one line so completed/export states do not flood the
  terminal with repeated placeholders.
- 2026-06-30 `928a008` `fix: choose adaptation mode before cocreate`: mode
  selection for novel adaptation now happens immediately after source
  preparation and before adaptation co-create. Validation passed for startup,
  TUI, headless, host/adapt/flow, CLI parsing, and local/global build sync;
  `internal/tools` still hit existing Windows TempDir cleanup failures only.
- 2026-06-30 `3bb376f` `feat: stabilize adaptation source preparation`: pulled
  latest upstream code from GitHub.
- 2026-06-30 `docs: record restart validation`: record successful local rebuild
  and restart after updating to the latest GitHub code.
- 2026-06-30 `docs: update git notes after latest pull`: refresh baseline
  after updating to the latest GitHub code.
- 2026-06-30 `68e1f33` `fix: auto-run simulate from palette`: pulled latest
  upstream code from GitHub.
- 2026-06-30 `docs: add project git maintenance notes`: record project GitHub
  push rule and baseline.

## Rollback Notes

- To inspect the latest upstream pull: `git log --oneline --decorate -5`.
- To inspect or revert PR-01 after commit, use `git show --stat <commit>` or
  `git revert <commit>`; do not touch `novel/`.
- To undo only the local notes commit after review, use `git revert <commit>`.
- Do not delete or stage `novel/` unless the user explicitly asks to track that
  local runtime/workspace directory.
