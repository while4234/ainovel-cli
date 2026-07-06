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
- Latest source/runtime change: current local commit
  `fix: preserve loaded novel analysis`:
  Completed loaded novels now no-op when Analyze is clicked, returning
  `analyzed=true` without starting source analysis. Prepared source snapshots
  with complete reports and foundation can backfill missing co-create dossier
  batches without re-splitting the TXT, so legacy novel-library entries that
  only lack `cocreate_dossier.json` / `cocreate_dossier_batches/` keep their
  single-chapter reports and sync back to the library after backfill. Source
  snapshot reset and novel-library load replacement now back up existing
  `meta/adaptation` first. Adapt co-create validation now accepts a complete
  prepared snapshot for the selected source path, preventing
  `selected adaptation source has not been analyzed` after loading a library
  novel. Runtime data repair was performed under
  `C:\Users\RondleLiu\.ainovel\novels-preview`: `术士手册` reports 1-946 were
  backed up and SHA-aligned to the current manifest so analysis resumes at
  chapter 947; `女神攻略`, `回档无限`, and `诡案组` project adaptation snapshots
  were restored from the novel library with backups. Current known library
  statuses: complete entries include `大学刑法课`, `国中理化课`, `回档无限：为她纯洁一身`,
  `娇妻美妾任君尝`, `梦中的女孩`, `我以我血挽山河`, `我有一座冒险屋`, `择天记`,
  and `这不是我印象中的女魔头`; `女神攻略` and `诡案组` are complete through
  reports/foundation and need dossier-only backfill. Validation passed for
  full `go test ./...`, focused Web UI Vitest, Vite build, `git diff --check`
  with CRLF warnings only, and local Web restart on `http://127.0.0.1:9898`
  as PID `30172`.
- Latest source/runtime change: current local commit
  `feat: stabilize long-form adaptation co-create and planner batches`:
  Adaptation co-create now uses an explicit `force_rebrief` path for the
  "重新分析资料包" action; ordinary follow-up comments merge as lightweight draft
  increments and do not rerun full-book briefing. Co-create decisions can be
  submitted in one bulk UI/API request, while resolved decisions are persisted
  together and merged into the draft without repeated full-draft rewrites.
  Long-form planner skeleton prompts now use a compact source-foundation digest
  that omits arc chapter details, tolerate a single returned batch object by
  wrapping it into `batches`, split oversized skeleton batches into 4-chapter
  detail sub-batches, and pass previous detail context forward for continuity.
  The Web UI assets were rebuilt and the default co-create timeout is now 180s.
  Validation passed for focused backend packages, Web UI Vitest, and Vite build;
  final restart is handled by the local Web restart step after commit.
- Latest source/runtime change: pending local changes
  `fix: repair source splitting and library backfill sync`:
  Adaptation source splitting now filters source-site duplicate `免费阅读`
  chapter headings and separator-delimited author-note blocks before chapter
  detection/body persistence, fixing the `术师手册` split that produced 1444
  noisy units and failed on empty `characters`. Web adaptation-analysis
  completion now treats only terminal `adapt.StageError` events as failed, so
  recoverable dossier warning/retry events no longer skip the existing
  novel-library refresh callback after final `StageDone`. The local
  `术师手册` project snapshot was rebuilt to 1382 chapters and stale artifacts
  from chapter 947 onward plus derived dossier/foundation caches were removed.
  Validation passed for focused and full `internal/host/imp` and
  `internal/entry/web` Go tests, splitter packaging check on `术师手册.txt`,
  runtime cleanup verification, and local Web rebuild/restart on
  `http://127.0.0.1:9898` as PID `59984`.
- Latest source change: pending local changes
  `fix: strip novel-library cocreate progress`:
  Web novel-library save/load now allow-lists adaptation source analysis and
  co-create dossier artifacts instead of copying runtime co-create progress.
  Library save omits `cocreate_intent.json`, `cocreate_briefing.json`,
  `cocreate_briefing_batches/`, proposals, plans, checks, and session files;
  library load also clears the target project's Web co-create checkpoint/log
  and resets the frontend co-create state before displaying the loaded
  snapshot. The existing `这不是我印象中的女魔头` library entry and the two
  affected local projects were cleaned so their snapshots show no active
  co-create state while adaptation analysis remains `done`.
  Validation passed for focused and full Web Go tests, full Web UI Vitest,
  Vite build, `git diff --check` with CRLF warnings only, local Web restart on
  `http://127.0.0.1:9898` as PID `57712`, and HTTP/file smoke checks confirming
  no forbidden co-create progress paths remain. This batch is not committed yet
  because the worktree already contains broad unrelated changes, including
  pre-existing edits in the same frontend/static bundle files.
- Latest source change: current local commit
  `fix: refresh adaptation cocreate briefing intent`:
  Adaptation co-create now derives the long-novel briefing intent from all
  current user planning turns plus resolved briefing decisions, not only the
  first user request. Web send/revise refreshes stale intent-driven briefing
  before the next adapt co-create response and blocks on any newly generated
  required decisions. The detailed staged adaptation proposal planner still
  runs only after the user starts generation from the final draft.
  Validation passed for
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/web ./internal/host/adapt`
  and `git diff --check` on the changed co-create/session files, with CRLF
  conversion warnings only.
- Latest source change: current local commit
  `fix: make adaptation co-create resumable`:
  Web adaptation co-create decisions now render as a one-question pager with
  vertical wrapping option buttons, and the backend no longer limits pending
  decisions to the first four. Failed co-create sessions stay active with a
  `恢复共创` action, and `/cocreate/resume` continues from the saved prompt,
  mode, source, resolved briefing decisions, and draft state instead of losing
  progress.
  Provider gateway failures (typed or HTML 500/502/503/504 responses) are
  detected, sanitized, and treated as failover/retry-eligible in runtime
  fallback, co-create stream generation, planner generation, and briefing
  generation. Adaptation co-create briefing now checkpoints before generation
  starts and writes an explicit `COCREATE/adapt_briefing` lifecycle event, so
  briefing failures appear in the event log rather than silently ending.
  Validation passed for focused provider/co-create/briefing tests, full
  `go test ./internal/retrypolicy ./internal/bootstrap ./internal/host ./internal/host/adapt ./internal/entry/web -count=1`,
  full Web UI Vitest, Vite build, `git diff --check` on changed source files,
  and local Web restart on `http://127.0.0.1:9898` as PID `62520`. Runtime
  snapshot confirmed `这不是我印象中的女魔头`
  (`untitled-novel-20260705003748-de4f76`) is resumable with 19/19 briefing
  decisions resolved and no pending decisions.
- Latest source change: current local commit
  `fix: unblock adaptation library save and cocreate shell`:
  Web adaptation novel-library save now separates row editability from submit
  readiness, so a freshly analyzed source with an empty library name can still
  accept a name before enabling the save action. The adaptation save row now
  labels itself as `保存当前小说到库` while the simulation-profile row keeps
  `保存当前画像到库`.
  Web co-create begin/send/revise/decision/commit/cancel no longer owns the
  global shell `busy` flag; long adaptation co-create briefing requests set a
  current-project busy state instead, so the left-side new-project flow stays
  available. Awaited co-create responses capture the project ID and are ignored
  after switching or creating another project to avoid stale state pollution.
  Validation passed for focused and full Web UI Vitest, Vite build, focused Go
  Web tests, `git diff --check`, local Web restart on
  `http://127.0.0.1:9898` as PID `63112`, and HTTP smoke checks for `/`,
  `/api/runtime`, and `/api/projects`.
- Latest integrated change: current local commit
  `fix: stabilize web cocreate and source foundation resume`:
  The final integration includes the concurrent Web/co-create review UI,
  workspace display, retry-configuration, prompt-style, and static Web asset
  changes plus resumable source-foundation aggregation for adaptation analysis.
  Source foundation merge now persists report-level and summary-level
  checkpoints under `meta/adaptation/source_foundation_batches`, validates them
  with source/report/prompt/batch signatures, resumes after restart from the
  last completed merge batch, and clears checkpoints after the final
  `source_foundation.json` save. Validation passed for focused source
  foundation packages, full Web UI Vitest, Vite build, and full `go test ./...`.
- Latest prompt change: pending local changes
  `style: harden anti-ai revision guidance`:
  Writer style prompts and the shared `anti-ai-tone` reference now explicitly
  target over-smooth prose, over-complete explanatory detail, too-regular
  subject rhythm, polished metaphor/gold-line habits, over-clear psychological
  self-defense, broken-memory realism, and functionalized side characters. The
  writer rewrite/polish prompt now adds a specific anti-AI revision path:
  identify paragraph-level problem clusters first, allow local structural
  deletion/reordering, avoid replacing old lists with new lists, and avoid
  swapping old gold-line endings for new ones.
  Validation passed for focused Go style/loading/Web style tests with the
  project-local Go 1.25.5 binary and `git diff --check` on the changed prompt
  files, with CRLF conversion warnings only. The broader workspace already has
  unrelated concurrent changes; this prompt-only batch was not committed or
  pushed here.
- Latest source change: current local commit
  `fix: stabilize adaptation analysis and co-create review`:
  Adaptation source foundation merge now batches by source-report rune budget
  and recursively merges partial foundations, so large novels no longer send a
  single oversize request that can trigger HTTP 413. Source dossier, briefing,
  and structural repair attempts now honor Web-configurable retry settings, and
  loading older novel-library entries missing current dossiers starts analysis
  backfill instead of reporting them fully analyzed.
  Ordinary co-create stores a planning review gate, exposes pending
  volume/chapter-outline review in the Web UI, and requires the user to approve
  before writer resume. Web project switching resets stream/event workbench
  state to prevent cross-project display leakage, stream rounds are clipped to
  avoid text overlap, and writer `draft_chapter` can infer a missing chapter
  only when the runtime context is uniquely safe. Validation passed for
  `go test ./...`, Web UI Vitest (96 tests), and Vite build. Local Web was
  rebuilt and restarted first on `http://127.0.0.1:9898` as PID `59568`.
- Latest source change: current local commit
  `fix: neutralize adaptation dossier generation`:
  Adaptation co-create dossiers now use prompt version `v2` and store neutral
  source facts only: main causality, plot threads, character arcs,
  world/continuity constraints, relationship signals, and evidence-backed
  heroine/ambiguity/couple signals. Dossier-stage adaptation advice is no
  longer requested, displayed, or saved; malformed/empty dossier JSON now gets
  in-call repair retries before failing. Dossier batches remain capped at 40
  chapters but also split early at roughly 120k source runes for long-chapter
  novels, and current checks validate every dynamic batch range/signature.
  Validation passed for focused adapt/store/host/web tests and full
  `go test ./internal/...`; local Web was rebuilt/restarted on
  `http://127.0.0.1:9898` as PID `60036`. Current project generated
  dossier/briefing artifacts for `project-20260705003734-24bbf4` were deleted
  so Analyze can regenerate v2 dossiers from preserved source reports.
- Latest source change: current local commit
  `fix: backfill adaptation dossier batches`:
  Adaptation source-analysis resume now backfills fixed 40-chapter co-create
  dossier batches before continuing with newly analyzed chapters, and refreshes
  those batches again whenever a non-final 40-chapter boundary is reached. The
  final all-book co-create dossier still assembles only after all source reports
  are complete. Web novel-library save/load actions now append `LIBRARY`
  host events, and long-form adaptation co-create briefing progress is emitted
  as `ADAPT` events. Validation passed for focused adapt/web Go tests and full
  `go test ./internal/...`; local Web was rebuilt/restarted on
  `http://127.0.0.1:9898` as PID `42772`, and `/api/runtime` returned healthy.
- Latest source change: pending local changes
  `fix: refresh project model provider renames`:
  Web global model/provider edits now propagate the edited
  `original_provider -> provider` ID change to closed project overlays and
  active project sessions. Project configs rewrite default, role, fallback, and
  provider-map references away from the old provider ID, refresh provider
  display labels from the saved global config, and active hosts reapply the
  updated model registry without requiring each project to be opened manually.
  Validation passed for full `go test ./...`, full Web UI Vitest, Vite build,
  current runtime project-config scan with zero unknown provider refs, and one
  connectivity test per configured provider/model combo.
- Latest source change: pending local changes
  `feat: resynthesize imported simulation profiles`:
  Web simulation UI now keeps the profile library controls at the top, with an
  inline profile-name input directly above the full-width "保存当前画像到库"
  action, next to the existing "上传 JSON 到库" flow. It no longer auto-opens or
  auto-starts any save flow when analysis completes, and the "加载画像" import
  section moved above the TXT/MD corpus list so it remains reachable after large
  uploads. Importing a `simulation_profile.v1` JSON into a project that already
  has a profile now first merges sources and source reports by fingerprint, then
  uses the existing batched simulation merge pipeline to re-synthesize
  `synthesis` with the architect model; large imports are compacted and split by
  report count/prompt size, with merge checkpoints saved after successful
  batches and cleared after final save. Validation passed for full Web UI tests,
  full `go test ./...`, Vite build, `git diff --check` with CRLF warnings only,
  and local Web restart on `http://127.0.0.1:9898` as PID `59192`. The final
  static bundle no longer contains the removed save-dialog markers.
- Latest source change: current local commit
  `fix: hydrate project event history on open`:
  Web project switching now hydrates the workbench from a JSON event-history
  endpoint before relying on the live SSE stream. The new
  `/api/projects/{id}/events/history?after=N` route is read-only, does not
  append snapshots, and returns replay metadata (`oldest_seq`, `latest_seq`,
  `history_limit`) alongside `WebEvent` rows. Opening a running project applies
  those events through the existing reducer and updates the SSE cursor, so the
  event panel shows historical ADAPT/SIMULATE rows immediately after switching
  projects instead of waiting for the next live event. Validation passed for
  focused and full Web UI tests, focused Go web tests, Vite build, local Web
  restart on `http://127.0.0.1:9898`, and Playwright switching across seven
  running projects with no `暂无事件` state.
- Latest source change: `20efdd9`
  `fix: preserve project events on open`:
  Web project switching now merges the loaded snapshot into the existing
  workbench instead of replacing the whole workbench. This preserves SSE
  history replay and current running event rows when `/events?after=0` returns
  before `/snapshot`, so switching to an already-running project immediately
  shows its events. Validation passed for the focused Web event/progress tests,
  full Web UI Vitest suite, and Vite build. Local Web restart was attempted but
  blocked by unrelated dirty simulation source files that currently break Go
  build; those files were left uncommitted.
- Latest source change: current local commit
  `fix: clarify web model config flow`:
  Web model configuration now separates existing-provider editing from a clean
  `+ 新建` model flow in the right sidebar's one-column layout. New drafts no
  longer inherit the selected provider's Base URL/API key/model and no longer
  show or send an agent/role selection; they register provider/model config via
  `select_after_save:false`, leaving default/coordinator/architect/writer/editor
  assignment to the existing model selectors. `provider key` is labeled as a
  local configuration ID rather than an API key, new model drafts default request
  timeout to 120 seconds, connectivity timeout to 12 seconds, and network retry
  count to 7, and save validation surfaces inline errors instead of appearing
  inert. Existing-provider test/discovery requests now pass `original_provider`
  through the configured-provider path so testing `deepseek` no longer fails
  with a duplicate-provider add-flow error. Existing-provider saves skip upstream
  probes for local-only edits such as ID/display-name/timeouts/candidate-pool
  changes, so an exhausted or invalid upstream token does not block local
  renames; model/Base URL/protocol/auth/API-key changes still probe. Validation
  passed for focused/full Web UI tests, focused Go host/web tests before and
  after Web build, Vite build, and `git diff --check` with CRLF warnings only.
  Local Web was rebuilt/restarted on `http://127.0.0.1:9898`; old PID `18908`
  was stopped, new PID is `54724`, and `/` plus `/api/models` returned HTTP 200.
- Latest source change: current local commit
  `feat: add adaptation cocreate briefing`:
  Adapt co-create now builds an intent-driven pre-draft briefing for oversized
  source novels, using persisted all-book dossier batches as input instead of
  sending the whole dossier into one draft call. The briefing is keyed by source
  signature, dossier prompt version, briefing prompt version, and user intent
  hash; pending required decisions block draft generation until the user answers
  them one at a time. Web and TUI start/proposal paths share the readiness guard,
  Web exposes a compact decision queue, and adapt prompts prefer the resolved
  briefing snapshot before falling back to the all-book dossier. Validation
  passed for Web UI tests/build, focused Go tests, full `go test ./internal/...`,
  and `git diff --check` with CRLF warnings only. Local Web was rebuilt and
  restarted on `http://127.0.0.1:9898` with runtime root
  `C:\Users\RondleLiu\.ainovel\novels-preview` as PID `59188`; `/`,
  `/api/runtime`, and `/api/models` returned HTTP 200.
- Latest source change: current local commit
  `feat: add adaptation cocreate dossier`:
  Adaptation source analysis now generates a resumable all-book co-create
  dossier from per-chapter source reports after source foundation, replacing
  the previous adapt co-create prompt that only exposed the first 30 chapter
  summaries. Web/TUI analysis progress includes the dossier stage; Web
  `analysis_status=done`, adapt co-create begin, and novel library save/load now
  require a current dossier. Adapt co-create uses an 8192-token output cap and
  rejects provider length truncation or incomplete XML before accepting drafts.
  Validation passed for Web UI tests/build, focused Go tests, full
  `go test ./internal/...`, and `git diff --check` with CRLF warnings only.
- Latest source change: branch `codex/project-runtime-model-fallback`, pending commit
  `feat: add project runtime model fallback`:
  Runtime model fallback is project-scoped and switches all project agents
  together without mutating the global default provider/model. Quota/auth/rate
  limit/overloaded runtime fallback errors switch candidates immediately, while
  network or stream interruptions use the configured per-model network attempt
  budget first. Web model configuration now uses one configure flow for adding
  and editing providers, including provider key/name changes, key replacement
  with blank-key preservation, Base URL/API/auth/proxy/timeouts, and candidate
  pool membership. Manual default model switches now cascade to coordinator,
  architect, writer, and editor routes. Validation passed for focused and full
  changed-package Go tests, full Web UI tests, Vite build, and
  `git diff --check` with CRLF warnings only. Local Web was rebuilt/restarted
  on `http://127.0.0.1:9898` with runtime root
  `C:\Users\RondleLiu\.ainovel\novels-preview` as PID `56176`; HTTP smoke
  checks for `/`, `/api/runtime`, and `/api/models` returned healthy responses.
- Latest source change: current local commit
  `fix: accept legacy timeline strings`:
  `domain.TimelineEvent` now accepts legacy JSON string entries by preserving
  the text as the event field, while normal object entries keep chapter, time,
  event, and character fields. This covers import/source-analysis `TIMELINE`
  sections, existing `timeline.json` files, and `commit_chapter` append rereads,
  preventing `json: cannot unmarshal string into Go value of type
  domain.TimelineEvent` from escaping as a global Web error banner. Validation
  passed for focused domain/import/store tests, full `go test ./...`, Web
  rebuild/restart on `http://127.0.0.1:9898` as PID `57820`, HTTP smoke checks,
  and `git diff --check` with CRLF warnings only.
- Latest source change: current local commit
  `fix: unblock concurrent web analysis controls`:
  Web simulation/adaptation analysis and simulation-profile library import now
  start as background project actions and return immediately, so long-running
  analysis does not keep browser requests open or exhaust the browser's
  per-origin connection pool while other projects are active. The React side
  panels restore per-project simulation/adaptation running state from snapshots
  and SSE host events, scope upload/analyze button disabling to the current
  project action instead of one global busy flag, and show the active project's
  default model route in the model panel. Runtime project configs for the
  user's current source/corpus projects were switched to
  `deepseek/deepseek-v4-pro`; missing uploaded source/corpus files were restored
  from `D:\ainovel\novel` and `D:\ainovel\novel\split_packages`.
  Validation passed for focused Web UI tests, full Web UI tests, Vite build,
  full `go test ./...`, `git diff --check` with CRLF warnings only, Web restart
  on `http://127.0.0.1:9898` as PID `55992`, and HTTP smoke checks for runtime,
  project listing, and the affected source/corpus project snapshots.
- Latest source change: current local commit
  `fix: restore simulation uploads after refresh`:
  Web snapshots now include uploaded simulation TXT/MD source files from each
  project's `simulate/` directory, and the React simulation panel restores that
  list after refresh/open instead of showing `暂无上传语料`. Project switching no
  longer waits on the global busy state before visually selecting the clicked
  project; stale project-open, SSE, simulation-analysis, and adaptation-analysis
  responses are ignored after the user switches elsewhere. Validation passed for
  full Web UI tests, Vite build, full `go test ./...`, and local Web restart on
  `http://127.0.0.1:9898` as PID `23060`.
- Latest source change: current local commit
  `fix: remove text source upload size limit`:
  Web TXT/MD source uploads no longer use the old 10 MiB per-file limit or the
  64 MiB multipart request cap. This applies to simulation/仿写语料 uploads,
  adaptation/小说改编 source uploads, and external novel imports. JSON simulation
  profile uploads still keep the 5 MiB per-file limit and 64 MiB multipart cap.
  Validation passed for focused Web upload regressions, full
  `./internal/entry/web`, full `go test ./...`, `git diff --check` with CRLF
  warnings only, Vite build via `restart-web.cmd`, and HTTP smoke checks on
  `http://127.0.0.1:9898`; local Web restarted as PID `53056`.
- Latest source change: current local commit
  `feat: allow parallel analysis and partial export`:
  Web export remains a read-only current-progress operation and can run without
  pausing or aborting writing/analysis. `ProjectSession` now tracks action kinds
  instead of one global action mutex, allowing adaptation source analysis to run
  concurrently with simulation profile analysis/import while still rejecting
  writing, continue, revision, proposal, and duplicate profile actions that
  would conflict. The React side panels no longer hold global `busy` for
  adaptation/profile analysis, so users can upload original text to the
  simulation-profile flow and start profile analysis while adaptation source
  analysis is still running. Validation passed for Web UI tests, Vite build,
  focused and full Go tests, and `git diff --check` with CRLF warnings only.
  The local Web service was rebuilt/restarted on `http://127.0.0.1:9898` with
  runtime root `C:\Users\RondleLiu\.ainovel\novels-preview` as PID `35376`;
  HTTP `/`, `/api/runtime`, and `/api/projects` returned 200.
- Previous source change: current local commit
  `feat: save exports through browser picker`:
  Web export changed from "server writes under project exports" as the primary
  UI path to a browser-save flow. `POST
  /api/projects/{id}/export/download` returns the exported TXT/EPUB bytes and
  metadata headers; the frontend opens the native save-file picker when
  available and writes the returned blob to the chosen location, with browser
  download fallback. The workbench stream view now keeps more recent tool
  output blocks and no longer clips long stream cards, so adaptation/rewrite
  summaries can be read fully inside the central workspace.
- Latest prompt change: current local commit
  `style: add punctuation anti-ai review criteria`:
  `assets/references/anti-ai-tone.md` now includes an explicit
  "标点与句式 AI 味" section. Writer/editor shared AI-tone guidance now treats
  repeated explanatory em dashes as a reviewable aesthetic issue and recommends
  short sentences, action pauses, environmental detail, or natural paragraph
  breaks instead.
- Latest source change: current local commit
  `fix: preview completed chapter revisions`:
  Completed-book single-chapter revision now shows the selected chapter in the
  central workspace. The Web API `GET /api/projects/{id}/chapters/{chapter}`
  reads the current final chapter body from `chapters/NN.md`, falls back to
  draft text when no final exists, and returns word count/source metadata. The
  React UI lazy-loads only the selected chapter, shares the existing right-pane
  selection state, and renders loading/error/empty/body states in the main
  work area. Validation passed for focused Web API/UI tests, the broader
  `./internal/entry/web ./internal/host/flow ./internal/tools ./internal/store`
  Go package set, UI build, `git diff --check` with CRLF warnings only, and a
  live smoke on `http://127.0.0.1:9898`; project `梦中的女孩3` returned 59 outline
  rows and chapter 1 final content through the new endpoint. The local Web
  service was rebuilt and restarted with runtime root
  `C:\Users\RondleLiu\.ainovel\novels-preview` as PID `40604`.
- Latest source change: current local commit
  `feat: add completed chapter revision backend`:
  Completed-book single-chapter revision is implemented across Web UI and
  backend orchestration. The Web API `POST /api/projects/{id}/chapters/revise`
  accepts a completed chapter, instruction, and `rewrite`/`polish` mode, then
  reopens the completed book through `PendingRewrites` and resumes the existing
  writer flow. The UI adds a completed-book chapter revision panel, with tests
  covering visibility, payload validation, and API wrapper behavior. Backend
  validation passed for `./internal/entry/web ./internal/host/flow
  ./internal/tools ./internal/store`; frontend validation passed for
  `npm --prefix internal/entry/web/ui test -- --run`.
- Latest prompt change: current local commit
  `style: tighten style prompt anti-ai guidance`:
  All embedded style prompts now add non-NSFW guidance against numbered
  report-like reasoning, frequent explanatory em dashes, and psychology written
  as model-style inference lists. Validation passed for focused assets tests
  with the repo-local Go toolchain and `git diff --check` with CRLF warnings
  only.
- Latest source change: branch `main`, current commit
  `fix: infer explicit cocreate word count`:
  Web normal co-create now inspects the initial idea for an explicit total word
  count such as `5000字`, `三万字`, `十万字`, or `30w字`. When present, it sends the
  inferred `target_total_words` directly with the first co-create request. When
  the user gives only a vague length label such as `长篇`, the Web UI opens the
  target-word confirmation step instead of mapping that label to a fixed number.
  Per-chapter counts such as `每章5000字` are ignored for total-budget inference.
  The local Web service was rebuilt and restarted on `http://127.0.0.1:9898`
  with runtime root `C:\Users\RondleLiu\.ainovel\novels-preview` and PID
  `43080`.
- Previous source change: `a03cab2`
  `fix: normalize web export file extension`:
  Web export resolves the selected format before normalizing user-provided
  filenames. Relative names without an export suffix get `.txt` or `.epub`
  appended from the selected format, so title-only inputs such as
  `梦中的女孩改` no longer produce extensionless files. Explicit `.txt`/`.epub`
  suffixes that conflict with the selected format are rejected before the host
  export is called.
- Previous docs checkpoint: `21eb5c6`
  `docs: record adaptation mode guidance fix`.
- Previous source change: `ee471d2`
  `fix: split adaptation mode guidance`:
  adaptation mode prompts and runtime writer context now separate
  `chapter/preserve_details`, `arc/full_rewrite`, and `free/full_rewrite`
  instead of mixing all mode rules into one prompt. `free` mode now carries an
  explicit `adaptation_effective_mode` contract: source refs are background
  anchors only, `preserve_details` is not applicable, and `read_chapter`
  against original source is optional fact-checking rather than a required
  per-chapter step. The Web static assets were rebuilt to include the
  source-label UI update.
- Previous source change: `976a1fb`
  `fix: hide free adaptation source labels`:
  proposal chapter cards/rows and staged volume review details no longer render
  original-source mapping labels such as `原 17-17` or `原作范围` for
  `granularity=free`; `chapter` and `arc` keep those labels.
- Previous source change: `916f7fc`
  `fix: label source chapter reads`:
  `read_chapter` events now distinguish original-source reads from generated
  chapter reads by displaying `·原文` for `source:"source"`, so adaptation
  reference calls like `read_chapter(第17章·原文)` no longer look like the
  writer is reading an old generated chapter while drafting chapter 52.
- Previous source change: `ffb3dc2`
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
- Working tree: clean after committed adaptation mode guidance and free-source
  label UI fixes, plus
  ignored local `.codex/`, `.playwright-mcp/`, and output/runtime artifacts.
- Runtime: local Web was rebuilt and restarted on `http://127.0.0.1:9898` from
  source commit `ee471d2`; the restarted process is PID `47552`.
- Validation:
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/startup ./internal/entry/tui ./internal/entry/web ./internal/host ./internal/tools -run "Test(DefaultAdaptationBrief|AdaptationModeContract|AdaptCoCreateOpener|AdaptModeConfirmStartsCoCreateWithSelectedMode|ContextToolClarifiesFreeFullRewriteSourceRefsAreAnchors|ContextToolShowsDisabledWordToleranceForFullRewrite|AdaptationFreePlanDoesNotRequireSourceRead|PreserveDetailsPlanAndDraftSteerTowardFullSourceBackedChapter|LoadKeepsAdaptationGuidanceOutOfBaseWriterPrompt|ProjectAdaptCoCreateCheckpointRestoresModeAndCommits)" -count=1`
  passed;
  `npm.cmd --prefix internal/entry/web/ui test -- src/workspace-progress.test.js --run`
  passed in the UI worker, 1 file / 23 tests;
  `npm.cmd --prefix internal/entry/web/ui test -- --run` passed, 6 files / 65
  tests;
  `npm.cmd --prefix internal/entry/web/ui run build` passed and produced
  `/assets/index-Bs5-fN-Z.js`;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./... -count=1`
  passed;
  `git diff --check` passed with CRLF warnings only;
  `.\restart-web.cmd -Port 9898` passed after prepending the project-local Go
  1.25.5 bin directory to PATH, rebuilding Web UI and Go executable, stopping
  PID `44864`, and starting PID `47552`;
  `GET /`, `GET /api/runtime`, `GET /api/projects`, and the active project
  snapshot returned HTTP 200. Project `untitled-novel-20260704010240-cccef4`
  restored as READY in writing phase with 53/59 chapters completed and recovery
  at chapter 54.

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

- 2026-07-05 current local commit `test: stabilize legacy library backfill test`:
  captured the fake adaptation-analysis start channel in the legacy
  novel-library load regression before issuing the request, avoiding a mutable
  field read in the select path.

- 2026-07-05 current local commit `fix: stabilize adaptation analysis and co-create review`:
  added recursive batched foundation aggregation, configurable model/structure
  retries, normal co-create planning approval UI, stream/workbench isolation
  fixes, safe writer chapter inference, and refreshed Web static assets.

- 2026-07-05 pending `fix: allow Grok OAuth save without OpenAI endpoint`:
  added Grok OAuth provider-config normalization in bootstrap/Web/host save
  paths and focused regression tests; rebuilt/restarted local Web PID `28572`;
  left uncommitted to avoid staging mixed pre-existing worktree changes.

- 2026-07-05 current local commit `feat: add adaptation cocreate dossier`:
  source analysis now builds an all-book adaptation co-create dossier in
  resumable batches from source reports, Web/TUI readiness waits for it, adapt
  co-create consumes it instead of first-30 summaries, adapt co-create output
  cap is 8192, and truncated/incomplete XML drafts are rejected. Validation
  passed for Web UI tests/build, full `go test ./internal/...`, and
  `git diff --check`.

- 2026-07-05 current local commit `fix: remove text source upload size limit`:
  removed application-layer size caps from Web TXT/MD source uploads for
  仿写语料, 小说改编 source analysis, and external novel import while keeping JSON
  simulation profile uploads bounded. Added regressions proving both simulation
  and adaptation source uploads accept files larger than the former 10 MiB
  limit, rebuilt/restarted the local Web service on `http://127.0.0.1:9898`, and
  verified HTTP `/`, `/api/runtime`, and `/api/projects`.

- 2026-07-04 current local commit `style: tighten style prompt anti-ai guidance`:
  Added three prose-quality bullets to each `assets/styles/*.md` prompt directly
  under the style section and before the NSFW section. The bullets discourage
  numbered possibility lists, overused explanatory `——`, and model-like
  psychological inference checklists.

- 2026-07-04 current commit `fix: infer explicit cocreate word count`:
  Normal Web co-create now skips the target-word confirmation only when the
  initial idea contains an explicit total word count. Vague labels such as
  `长篇` still require user confirmation because `30万字` and `50万字` can both be
  long-form targets. Validation passed for all Web UI tests, Vite build, focused
  co-create backend tests, full `internal/entry/web` tests, and `git diff
  --check` with CRLF warnings only. The local Web service was rebuilt/restarted
  on port `9898` after prepending repo-local Go to PATH, and `/api/runtime` plus
  `/api/projects` returned HTTP 200.

- 2026-07-04 `a03cab2` `fix: normalize web export file extension`:
  Web export appends the selected `.txt`/`.epub` suffix when users enter a
  title-only filename, rejects `.txt`/`.epub` suffixes that conflict with the
  selected format, and keeps all paths scoped under the project `exports`
  directory. Validation passed for focused Web export tests, export renderer
  tests, full `internal/entry/web` tests, and the restarted local Web service
  on `http://127.0.0.1:9898` (PID `45780`).

- 2026-07-04 `ee471d2` `fix: split adaptation mode guidance`:
  splits adaptation prompting/runtime guidance by effective mode so
  `free/full_rewrite` no longer inherits chapter-only `preserve_details`
  wording. `novel_context` now exposes `working_memory.adaptation_effective_mode`
  with source-ref semantics, `plan_chapter` treats free source refs as optional
  background anchors, co-create openers no longer inject the mixed
  `rewrite_policy_rule=chapter=>...;arc/free=>...` line, and embedded Web assets
  were rebuilt after the UI source-label fix.

- 2026-07-04 `976a1fb` `fix: hide free adaptation source labels`:
  hides original-source mapping labels such as `原 17-17` and `原作范围` from
  free-mode proposal chapter cards/rows and volume review details while keeping
  source coverage data and preserving labels for chapter/arc modes.

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

