# AGENTS.md

## Agent skills

### Issue tracker

Issues live as markdown files under `.scratch/<feature-slug>/` in this repo. See `docs/agents/issue-tracker.md`.

### Triage labels

The five canonical roles, each label string equal to its name. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: `CONTEXT.md` and `docs/adr/` at the repo root. See `docs/agents/domain.md`.

## Pushes and tags

Once work is committed and tests pass, push `main` and push the next free
date-shaped tag without asking, then watch CI. Never move, delete or force-push
an existing tag, and never re-run `workflow_dispatch` over a published image.
See `.github/workflows/base-image.yml`.
