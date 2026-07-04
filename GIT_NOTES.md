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

- Latest source change: branch `main`, `ffb3dc2`
  `fix: repair malformed tool call args`:
  added a deterministic blind `ToolCallRepairModel` wrapper for
  architect/writer/editor/coordinator models. It repairs only lossless JSON
  object tool-call framing mistakes such as `{}""` or `{"chapter":1}""` before
  agentcore validation, while truncated JSON stays invalid so the originating
  model must retry with real content. `save_review` now opts into strict schema
  with required empty-default fields, and observer events downgrade the first
  malformed tool-argument validation failure per agent/tool to a yellow warning
  before escalating repeated consecutive failures to red errors.
- Previous source change: `1824d24`
  `fix: validate cocreate suggestions and dedupe volume review`:
  Web co-create suggestion buttons now only render backend-provided suggestions.
  When the streamed co-create reply omits structured suggestions, the backend
  runs a bounded JSON-mode model judge to decide whether real user-clickable
  suggestions exist; generic planning prose and "is this expected" follow-ups
  return no buttons. The staged adaptation volume-review UI also deduplicates
  mirrored `VolumeReviewSummary` and `AdaptationVolumeReview` volumes before
  rendering selectors, so each planned volume appears once while preserving the
  detailed review payload.
- Previous source change: `a9c0485`
  `fix: reject collapsed adaptation cocreate drafts`:
  adaptation co-create now treats placeholder drafts such as
  `同前轮（完整保留）`, `上一轮`, or previous-draft references as incomplete
  instead of letting them become the canonical proposal brief. Bad drafts still
  enter the existing model repair path; if repair fails, the session rolls back
  to the previous stable draft and blocks proposal generation until the latest
  user turn is consolidated. The Web adapt co-create entry also falls back to
  the current adaptation brief when the side-panel input is empty, avoiding an
  empty initial direction from the generic Adapt button.
- Previous source change: `99e66b4`
  `fix: let adaptation volume planner choose expansion`:
  selected-volume proposal revisions now always give the volume skeleton model
  an explicit `expansion_decision` choice (`expand` or `keep`) instead of using
  local keywords to force expansion. Detail batches are validated before merge;
  missing or invalid `word_budget`/chapter fields now trigger the existing
  batch repair loop instead of failing only at final proposal validation.
- Previous source change: `55c4162`
  `fix: enforce adaptation volume expansion revisions`:
  selected-volume proposal revisions now distinguish optional expansion from
  required expansion. If the user's volume revision asks to add/supplement plot
  content, a returned volume skeleton must increase `target_to`; unchanged
  chapter counts are repaired or rejected. Revised volume title/theme/goal/
  summary metadata is also written back to the saved proposal volume.
- Previous source change: `44e0986`
  `fix: expand adaptation volume revisions`:
  selected-volume proposal revisions now re-plan the volume skeleton first,
  then regenerate detailed chapter outlines; volume expansion shifts later
  chapters and later volume ranges by the added count.
- Previous source change: `2379137`
  `fix: dedupe adaptation proposal volumes in web ui`.
- Previous docs checkpoint: `051f4c7`
  `docs: record adaptation volume dedupe validation`.
- Previous source change: `af1b10d`
  `fix: remove stage wording from adaptation guidance`.
- Previous source change: `2f68cc2`
  `fix: stabilize adaptation proposal coverage`.
- Previous source change: `331162c`
  `fix: restore web cocreate action controls`.
- Earlier planner stability checkpoint: `411c1df`
  `fix: retry adaptation planner calls`.
- Branch: `main`
- Remote: `origin` -> `https://github.com/while4234/ainovel-cli.git`
- Working tree: clean after committed co-create suggestion and volume-review UI
  fixes, plus
  ignored local `.codex/`, `.playwright-mcp/`, and output/runtime artifacts.
- Runtime: local Web was rebuilt and restarted on `http://127.0.0.1:9898` from
  commit `1824d24`; the restarted process is PID `39972`.
- Validation:
  `npm.cmd --prefix internal/entry/web/ui test -- src/cocreate.test.js src/workspace-progress.test.js --run`
  passed;
  `npm.cmd --prefix internal/entry/web/ui test -- --run` passed, 6 files / 63
  tests;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/host -run "Test(CoCreateStream|ParseSuggestion)" -count=1`
  passed;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/web ./internal/host -count=1`
  passed;
  `npm.cmd --prefix internal/entry/web/ui run build` passed;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./... -count=1`
  passed after rerunning it post-build, because the first parallel build/test
  attempt raced Vite asset replacement with Go embed enumeration;
  `git diff --check` passed with CRLF warnings only;
  `.\restart-web.cmd -Port 9898` passed after prepending the project-local Go
  1.25.5 bin directory to PATH, rebuilding Web UI and Go executable, stopping
  PID `37084`, and starting PID `39972`;
  `GET /`, `GET /api/runtime`, and `GET /api/projects` returned HTTP 200, and
  `/` points at `/assets/index-1-e7YXNx.js`.

  Previous validation:
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/web -run "TestProjectAdaptCoCreate(CommitRepairsPreviousRoundPlaceholderDraft|RollsBackRejectedDraftWhenRepairFails|CommitRepairsRestoredRegressedDraft|RepairsRegressedDraftBeforeProposal|RepairsStaleDraftBeforeProposal)$" -count=1 -v`
  passed;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/web -count=1`
  passed;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/web ./internal/host ./internal/host/adapt -count=1`
  passed;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./...` passed;
  `npm.cmd --prefix internal/entry/web/ui test -- src/workspace-progress.test.js --run`
  passed;
  `npm.cmd --prefix internal/entry/web/ui test -- --run` passed, 6 files / 64
  tests;
  `npm.cmd --prefix internal/entry/web/ui run build` passed;
  `git diff --check` passed with CRLF warnings only;
  `.\restart-web.cmd -Port 9898` initially failed because `go` was not on PATH,
  then passed after prepending the project-local Go 1.25.5 bin directory,
  rebuilding Web UI and Go executable, stopping PID `5276`, and starting PID
  `37084`;
  `GET /`, `GET /api/runtime`, and `GET /api/projects` returned HTTP 200.

  Earlier validation:
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/host/adapt -run "TestBuildAdaptationProposal(FreeDefaultsLongSourceToChunkedPlanner|CoversSparseSourceAnchorsByExplicitRange|ClearsChunkedRuntimeAfterFinalValidationFailure|FreeUsesChunkedPlannerForLongTargetChapters|ResumesChunkedPlannerRuntimeAfterBatchFailure)$" -count=1 -v`
  passed;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/host/adapt -count=1`
  passed;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/web -run TestProjectSessionBuildAdaptationProposal -count=1`
  passed; `git diff --check` passed with CRLF warnings only;
  `.\restart-web.cmd -Port 9898` rebuilt and restarted Web twice during live
  validation, most recently stopping PID `34544` and starting PID `41644`;
  `GET /api/runtime` and `/api/projects` returned HTTP 200.
  Live retry for project `untitled-novel-20260703122424-2e6c6b` generated an
  18-chapter free/full_rewrite proposal covering all 17 source chapters,
  confirmed it into `plan.json`, and started writing chapter 1
  (`RuntimeState=running`, `Phase=writing`, `InProgressChapter=1`).
  Focused wording validation passed:
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./assets ./internal/tools -run 'TestLoadKeepsAdaptationGuidanceOutOfBaseWriterPrompt|TestContextToolInjectsAdaptationWriterGuidanceOnlyForAdaptation'`.
  Volume dedupe validation passed:
  `npm test -- src/workspace-progress.test.js`;
  `npm run build`;
  `.\restart-web.cmd -Port 9898` rebuilt the Web UI/Go executable, stopped PID
  `28960`, and restarted Web on `http://127.0.0.1:9898` as PID `11268`;
  Playwright verified the live project proposal still has 60 chapters / 5 stored
  volumes and the UI shows `全卷` plus exactly 5 volume options, with exactly 5
  proposal workspace volume blocks.

  Volume revision validation passed:
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/host/adapt -count=1`;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/web ./internal/host ./internal/host/adapt -count=1`;
  `git diff --check` passed with CRLF warnings only;
  `.\restart-web.cmd -Port 9898` rebuilt the Web UI and Go executable, stopped
  PID `16704`, and restarted Web as PID `31592`;
  snapshot structure check for project `untitled-novel-20260703215407-d41d36`
  confirmed the existing proposal is still status `proposal`, 60 chapters, 5
  volumes, last volume `47-60`.

  Required-expansion follow-up validation passed:
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/host/adapt -run "TestReviseAdaptationProposal" -count=1`;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/host/adapt -count=1`;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/web ./internal/host ./internal/host/adapt -count=1`;
  `git diff --check` passed with CRLF warnings only;
  `.\restart-web.cmd -Port 9898` rebuilt the Web UI and Go executable, stopped
  PID `31592`, and restarted Web as PID `10368`;
  `GET /api/runtime` and `/api/projects` returned HTTP 200;
  current user project `untitled-novel-20260703215407-d41d36` still has saved
  proposal status `proposal`, 60 chapters, 5 volumes, last volume `47-60`.

  Model-decision expansion follow-up validation passed:
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/host/adapt -run "TestReviseAdaptationProposal" -count=1`;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/host/adapt -count=1`;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/web ./internal/host ./internal/host/adapt -count=1`;
  `git diff --check` passed with CRLF warnings only;
  `.\restart-web.cmd -Port 9898` rebuilt the Web UI and Go executable, stopped
  PID `10368`, and restarted Web as PID `5276`;
  `GET /api/runtime` and `/api/projects` returned HTTP 200;
  current user project `untitled-novel-20260703215407-d41d36` still has saved
  proposal status `proposal`, 60 chapters, 5 volumes, last volume `47-60`.

## Change Log

- 2026-07-04 `1824d24` `fix: validate cocreate suggestions and dedupe volume review`:
  co-create suggestion chips no longer come from frontend prose/keyword
  guessing. Backend structured suggestions remain authoritative, and empty
  suggestion sets get one bounded JSON-mode model judge pass that returns either
  short user-voice suggestions or an empty list. Staged adaptation volume review
  now deduplicates mirrored summary/review volumes before rendering the volume
  selector, with frontend/backend regressions and regenerated embedded Web
  assets.

- 2026-07-04 `a9c0485` `fix: reject collapsed adaptation cocreate drafts`:
  adaptation co-create now rejects placeholder drafts such as `同前轮（完整保留）`
  or previous-round references before they can become the proposal brief. The
  session asks the model to repair incomplete drafts, rolls back to the previous
  stable draft if repair fails, keeps proposal generation blocked until the
  latest user turn is consolidated, and sends the current adaptation brief when
  the generic side-panel Adapt button starts co-create with an empty input.

- 2026-07-04 `99e66b4` `fix: let adaptation volume planner choose expansion`:
  selected-volume proposal revisions now ask the model to return
  `expansion_decision:"expand"` or `"keep"` during volume re-planning, and the
  backend validates that the decision matches the returned `target_to`.
  Volume/detail batch validation now runs before merge/save so missing
  `word_budget` and related chapter contract issues enter the repair loop
  instead of surfacing only as final validation failures. Failed revisions still
  do not overwrite the last saved proposal.

- 2026-07-04 `55c4162` `fix: enforce adaptation volume expansion revisions`:
  when a selected-volume proposal revision explicitly asks to add/supplement
  plot content, the volume skeleton must expand beyond the original `target_to`
  before detailed chapters are generated; unchanged counts trigger repair and
  then rejection. The revised volume metadata is persisted so the proposal
  workspace shows the new volume introduction after a successful re-plan.

- 2026-07-04 `44e0986` `fix: expand adaptation volume revisions`:
  proposal revision for a selected volume now runs in two stages: first re-plan
  the volume skeleton/plot range, then regenerate detailed chapter outlines for
  the revised volume. If the volume-level instruction asks to add/expand
  chapters, the selected volume can grow and all following chapter numbers and
  volume ranges shift by the added count. Fixed single-chapter/range revisions
  still reject count changes, and no-change revisions are rejected instead of
  being saved as successful.

- 2026-07-04 `2379137` `fix: dedupe adaptation proposal volumes in web ui`:
  front-end proposal review now deduplicates summary/proposal mirrored volumes
  by chapter range before rendering revision options and workspace sections;
  includes a Vitest regression and rebuilt embedded Web assets.
- 2026-07-04 `af1b10d` `fix: remove stage wording from adaptation guidance`:
  replace "舞台提示" with "提示性括注" and "舞台大小" with "故事规模"; confirmed no
  `舞蹈` / `跳舞` / `舞台` / word-boundary `dance` / `dancing` terms remain in
  tracked project text outside historical Git notes.
- 2026-07-03 current `HEAD` `fix: stabilize adaptation proposal coverage`: default
  long source-backed `arc`/`free` proposals into chunked planning when no
  explicit target chapter count is present, count explicit `source_range`
  coverage and expand sparse `source_chapters` before saving, clear invalid
  chunked proposal runtime checkpoints on final validation failure, add
  regression tests for the `does not cover source chapter` failure class,
  rebuild/restart Web on `http://127.0.0.1:9898` as PID `41644`, and verify the
  failed project now has a confirmed 18-chapter adaptation plan and active
  chapter-1 writing run.
- 2026-07-03 `331162c` `fix: restore web cocreate action controls`: consumes
  backend `can_start` in the React co-create state, disables launch until the
  draft is fresh/startable, parses paragraph-style AI choice questions into
  clickable suggestions, keeps long right-pane draft previews scrollable so
  action buttons remain visible, rebuilds embedded Web assets, restarts local
  Web on `http://127.0.0.1:9898` as PID `36560`, and verifies UI/backend paths.
- 2026-07-03 `pending` `docs: record latest restart check`: fetched
  `origin/main` with no new commits beyond the current branch, rebuilt the Web
  UI and Go executable, restarted local Web on `http://127.0.0.1:9898` as PID
  `15252`, and verified `/`, `/api/runtime`, and `/api/models`.
- 2026-07-03 `pending` `docs: record latest pull and restart`: fast-forwarded
  `main` from `e838341` to `21200be`, rebased the maintenance checkpoint after
  `origin/main` advanced to `861b527`, rebuilt the Web UI and Go executable,
  restarted local Web on `http://127.0.0.1:9898` as PID `26304`, verified `/`,
  `/api/runtime`, and `/api/models`, and recorded that restart commands in this
  shell need the project-local Go 1.25.5 bin directory prepended to `PATH`.
- 2026-07-03 `411c1df` `fix: retry adaptation planner calls`: wraps adaptation
  planner model calls in the shared 7-attempt retry policy with visible progress
  messages, keeps malformed-JSON/schema repairs bounded, fills missing
  `word_budget` data locally from legacy budget fields or source chapter anchors,
  adds regression tests for transient model failure and missing chapter budgets,
  restarts `http://127.0.0.1:9898`, and verifies a safe 20-chapter / 4-volume
  proposal plus single-chapter, range, and volume proposal revisions.
- 2026-07-03 `1ed3100` `fix: stabilize long-form adaptation proposal batches`:
  keeps model-chosen long-form skeleton/volume planning but splits oversized
  detail ranges into at most eight chapters per model call, fills partial
  missing chapter returns by sending accepted chapters plus the previous model
  output back to the model, limits repair attempts, adds detailed Web ADAPT
  progress/warn events for proposal generation and revision, recomputes totals
  after proposal revisions, restarts `http://127.0.0.1:9898`, and verifies with
  `go test ./...`.
- 2026-07-03 `389b0ab` `feat: add adaptation proposal review UI`: moves saved
  adaptation proposals into the central workbench, adds readable chapter cards,
  right-side single-chapter/range/whole-volume proposal revision controls,
  preserves saved proposals across refresh even when the upload source cannot be
  reconstructed, expands frontend/API tests, regenerates embedded Web assets,
  restarts `http://127.0.0.1:9898`, and verifies the UI with Playwright on a
  safe test project. The later two-stage volume-review workflow remains a
  follow-up checkpoint.
- 2026-07-03 `checkpoint` `checkpoint: persist adaptation proposal review foundation`:
  adds durable proposal volume metadata, saves model-planned long-form batches
  as proposal volumes, uses saved volumes when confirming adaptation plans into
  layered outlines, adds backend/session/API plumbing and visible events for
  proposal revision, starts front-end revision state/API wiring, and regenerates
  embedded Web assets. The later two-stage volume-review workflow remains a
  follow-up PR/checkpoint.
- 2026-07-03 `78c3fa6` `fix: chunk long-form adaptation planner proposals`:
  added target chapter-count inference for adaptation briefs, routed long
  `arc/free` proposals through a model-driven skeleton plus batch-detail
  planner flow, preserved strict proposal validation and no-activation proposal
  semantics, rejected skeletons that ignore long-form scale, covered 20+ and
  50-60 chapter regressions, restarted local Web, and verified the current
  restored co-create state without triggering unsafe generation.
- 2026-07-03 `5c0d9a4` `fix: preserve multi-turn adapt cocreate drafts`:
  tracks co-create draft freshness, repairs missing/stale/regressed drafts,
  keeps all user planning turns in repair context, runs a final consolidation
  before multi-turn adapt proposal generation, clears stale adapt co-create
  checkpoints, and rejects planner no-chapter output instead of silently saving
  a fallback proposal.
- 2026-07-03 `07eeaae` `fix: make adapt cocreate proposal start robust`:
  restored Web adapt co-create mode/source state across refresh/restart,
  changed adapt co-create commit to build a proposal instead of silently doing
  nothing, made planner parsing tolerant of common JSON shapes, added a
  deterministic proposal fallback for unusable planner output, synchronized the
  frontend proposal key after co-create commit, and regenerated embedded Web
  assets.
- 2026-07-02 `pending` `feat: add web model deletion`: added global and
  project-scoped model deletion, protects the active default route, removes
  deleted routes from role/fallback config, refreshes in-memory model routing,
  persists updated config, exposes Web delete controls, rebuilds embedded Web
  assets, restarts `http://127.0.0.1:9898`, and verifies backend/frontend/live
  browser behavior.
- 2026-07-02 `ff9d2c4` `docs: record latest update and restart`: fetched
  latest `origin/main` from `79703a9` to `bba6eea`, rebased the local
  maintenance commit, rebuilt and restarted the local Web service via
  `.\restart-web.cmd` with the project-local Go 1.25.5 toolchain on PATH,
  stopped old PID `89184`, started PID `236716`, and verified
  `http://127.0.0.1:9898` plus `/api/runtime`.
- 2026-07-02 `fef41a9` `docs: record latest pull and restart`: pulled latest
  `origin/main` with `git pull --ff-only`, fast-forwarded to `79703a9`, rebuilt
  and restarted the local Web service via `scripts\restart-web.ps1 -Port 9898`,
  verified `http://127.0.0.1:9898`, `/api/runtime`, `/api/projects`, and
  confirmed port 9904 had no listener.
- 2026-07-02 `a83b2af` `feat: surface adaptation planning state in UI`:
  completed the strict-agent adaptation proposal/UI pipeline after independent
  review and fix rounds. The sequence added additive planning contracts
  (`c772872`), real `arc/free` planner-backed proposals (`0e91abc`), explicit
  build/confirm/start flow (`91be2eb`), persisted hard/soft adaptation word
  budgets (`1cccc2f`), and snapshot/UI visibility for loaded profiles,
  creative blueprints, proposals, full outlines, budget/source coverage, and
  safe stale-proposal handling (`a83b2af`). UI assets were rebuilt.
- 2026-07-02 `074fd6b` `fix: stabilize web short story creation`: changed
  normal creation word-budget misses and repeated-draft detection from hard
  local errors into structured tool feedback, added draft-stage word-budget
  guidance, normalized one-entry outline chapter numbers, removed remaining
  Ctrl+S start copy from Web co-create responses, and verified a real 5000-word
  one-entry Web co-create completed without local ERROR.
- 2026-07-01 `af33ae7` `fix: add global web model providers`: added
  `POST /api/models/add`, enabled no-project `Add and use` for global provider
  registration, allowed logged-in Grok OAuth to become visible in the global
  default model selector after confirmation, added backend/frontend coverage,
  regenerated embedded Web assets, rebuilt the local binary, and restarted port 9898.
- 2026-07-01 `aba5224` `feat: add web default model control`: added global
  `/api/models` and `/api/models/default` endpoints, synchronized server/session
  config snapshots for newly opened projects, added the Web "瑜版挸澧犳妯款吇濡€崇€? selector,
  kept Grok callback actions usable outside global busy state, added tests,
  regenerated embedded Web assets, rebuilt the local binary, and restarted port 9898.
- 2026-07-01 `6a52385` `fix: allow global web grok login`: added global Web
  Grok OAuth start/poll/complete/status endpoints, routed frontend Grok login
  calls to them when no project is active, kept Login/Poll/Status clickable
  without an open project, added backend/frontend coverage, regenerated embedded
  Web assets, rebuilt the local binary, and restarted port 9898.
- 2026-07-01 `5c9ff38` `fix: open web grok oauth login`: added an opt-in
  backend system-browser opener for Web Grok OAuth login start, frontend pending
  and no-project feedback, popup/manual-link fallbacks, API/backend tests, and
  regenerated embedded Web static assets.
- 2026-07-01 `7bee635` `feat: add word budget progress UI`: added workspace progress
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
