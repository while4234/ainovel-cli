# Git Notes

## Repository Purpose

This repository contains the `ainovel-cli` Go command line application and its
supporting assets, documentation, scripts, and tests.

## Ignore And Secret Policy

Local executables, generated build/release artifacts, workspaces, output
directories, editor files, and local `.ainovel/` configuration are ignored.
Never commit provider credentials, API keys, `.env` values, private keys,
cookies, or raw local auth paths. Safe example configuration files may be
tracked when they contain placeholders only.

## Project GitHub Rule

For this project, a completed future development task is not done until the
intentional changes are committed and pushed to the configured GitHub remote
(`origin`) unless the user explicitly says not to push for that task.

## Current Baseline

- Latest upstream source commit: `3bb376f` `feat: stabilize adaptation source preparation`
- Branch: `main`
- Remote: `origin` -> `https://github.com/while4234/ainovel-cli.git`
- Working tree: has known untracked local workspace directory `ds_xfk/`
- Validation: latest pull and compile-only tests succeeded on 2026-06-30;
  restart/build smoke pending in the current maintenance pass

## Change Log

- 2026-06-30 `3bb376f` `feat: stabilize adaptation source preparation`: pulled
  latest upstream code from GitHub.
- 2026-06-30 `docs: update git notes after latest pull`: refresh baseline
  after updating to the latest GitHub code.
- 2026-06-30 `68e1f33` `fix: auto-run simulate from palette`: pulled latest
  upstream code from GitHub.
- 2026-06-30 `docs: add project git maintenance notes`: record project GitHub
  push rule and baseline.

## Rollback Notes

- To inspect the latest upstream pull: `git log --oneline --decorate -5`.
- To undo only the local notes commit after review, use `git revert <commit>`.
- Do not delete or stage `ds_xfk/` unless the user explicitly asks to track that
  local workspace.
