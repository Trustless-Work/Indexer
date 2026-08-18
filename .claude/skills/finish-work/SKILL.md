---
name: finish-work
description: Closing package for a finished unit of work in this repo — commit message, PR title/body and merge title/body, in English, pasteable as-is. Use when the user says the work is done, asks to close a sprint/task, or asks for a commit/PR message.
---

# Finish work — the closing package

The user commits, pushes and merges by hand. Your deliverable is the TEXT, complete and
in this exact order. A one-line commit suggestion alone is an incomplete deliverable.

## 0. Preconditions (run, do not assume)

- `gofmt -l .` prints nothing; `go vet ./...`, `CGO_ENABLED=0 go build ./...`, `go test ./...` are green.
  If any is red, say so with the output and stop — do not produce the package.
- `git status` shows only intended files. Mention anything unexpected.
- `CHANGELOG.md` `[Unreleased]` has an entry if the change is user-visible or operational.
- The branch is NOT `main`. If it is, stop: work must land via PR.

## 1. Commit — repo name in the heading

Give the commands verbatim:

```bash
git add .
git commit -m "<subject>

<body>"
git push -u origin <branch>
```

Rules for the `-m` string — it is pasted into a shell:
- **No double quotes, single quotes, apostrophes or backticks anywhere inside it.**
  Write "do not" instead of "don't"; name identifiers plainly without backticks.
- Subject: conventional commit (`feat|fix|chore|docs|security|refactor|test(scope): ...`),
  imperative, ≤ 72 chars.
- Body: one paragraph of reasoning/scope decisions; then one bullet per change; a
  `Deferred:` section if anything was consciously left out; a final `Tests:` line
  (packages tested + gofmt/vet clean).
- If the work maps to audit finding IDs or a sprint tag, name them in the body.

## 2. Pull Request — title

Same conventional prefix as the commit subject; add the sprint tag if there is one.

## 3. Pull Request — description

Sections, in order (quotes and backticks are fine here):

- `## Context` — why now, predecessors, scope decisions.
- `## What it resolves` — bullet per change; bold the finding ID when there is one.
- `## Deferred` — what was left out and why (omit section if nothing).
- `## Testing` — what was run and the result.

## 4. Merge — title and description

Title = PR title. Description = 3–5 line summary suitable for `git log`.

## Hard rules (repo-wide)

- **English** for every git artefact, even if the conversation is in Spanish.
- **No AI attribution anywhere**: no `Co-Authored-By: Claude`, no "generated with",
  no mention of assistants in commit, PR, merge or code. The work must read as fully human.
- Close with the status: what is merged/pending and what the next step is.
