# Headless `claude` and `codex`: what the CLIs actually give us

Type: research
Status: resolved
Blocked by:

## Question

Almost every other ticket assumes something about how the agent CLIs behave when
run non-interactively inside a container. Establish the facts for **both**
`claude` and `codex`, and note where they diverge, because the Agent YAML has to
abstract over both.

- How is a single non-interactive run invoked, and how is the prompt passed —
  argument, stdin, or file?
- Is there a structured event stream (JSONL or similar), what events does it
  emit, and is it complete enough to reconstruct what the run did? This is the
  Journal's raw material.
- What limit flags exist? A turn or step cap, a token cap, a wall-clock cap.
  Charting assumed turns pass straight through to a CLI flag — confirm that
  holds for both, and say what to do if `codex` has no equivalent.
- What are the exit codes, and can a caller distinguish "finished" from "hit its
  limit" from "crashed"?
- What does each CLI write to disk during a run, and where? Session state,
  caches, credentials.
- How is each authenticated in a headless container, and what does that need
  from the secrets path?
- Where does each look for skills on disk, and does that match where
  `skills add -g -a <agent>` puts them?

Record versions — this is the kind of fact that goes stale.

## Answer

Full findings: [`research/01-headless-claude-and-codex.md`](../research/01-headless-claude-and-codex.md).
~1290 lines, source-verified against `rust-v0.151.0`, with six items honestly
marked UNKNOWN.

**The headline invalidates a charting decision: neither CLI has a usable turn
abstraction.** `claude` has `--max-turns`, but it is now hidden from `--help` and
may be on its way out. `codex` has no turn, step, or tool-call cap at all —
confirmed by a repo-wide grep at the pinned tag. What it does have is
`features.rollout_budget`, a weighted **token** budget that genuinely hard-stops
a run, verified live:

```
codex exec -c 'features.rollout_budget.enabled=true' \
           -c 'features.rollout_budget.limit_tokens=4000' \
           -c 'features.rollout_budget.reminder_at_remaining_tokens=[1000]'
→ {"type":"turn.failed","error":{"message":"shared rollout token budget exhausted"}}, exit 1
```

So both CLIs have a *budget* limit and neither has a turn limit. **The Agent
YAML should take a token budget, not a turn count.** Caveat worth respecting:
`rollout_budget` is `Stage::UnderDevelopment`, off by default, undocumented, both
sub-keys are mandatory when enabled, and whether it survives the next release is
UNKNOWN. `agents.job_max_runtime_seconds` is a dead key — the source comments it
as a retained no-op.

**A codex network partition retries forever.** `ConnectionFailed` is classified
retryable, with no timeout and no exit. This promotes the container time-kill
from a policy choice to a **mandatory** backstop — and, with ticket 03's finding
that the control plane cannot kill what it cannot reach, makes the Run's internal
self-limit mandatory too.

**Exit codes discriminate less than assumed.**

- **Sandbox denials exit `0`.** In exec mode approvals are auto-rejected, the
  model works around the denial, and the turn completes. To know a Run was
  blocked, scan the items, not `$?`.
- **`error_seen` is sticky** — one non-retryable `error` event forces exit 1 even
  if the turn later completes successfully.
- **`SIGINT` exits 1 and emits no terminal event**; the stream simply stops.
  `SIGTERM`, which is what Teardown sends, has no handler on the exec path, hence
  the clean 143.
- **Codex's structured error codes exist and are thrown away.** `CodexErrorInfo`
  carries `SessionBudgetExceeded`, `UsageLimitExceeded`, `ContextWindowExceeded`,
  `Unauthorized` — and the exec mapper flattens all of it to `{message: String}`.
  Distinguishing "hit its limit" from "crashed" structurally requires
  `codex app-server`. Recorded as a known ceiling; not pursued for v1.

**The `--json` event stream is complete enough for the Journal**, and its schema
is captured from source: 8 event types (including `item.updated`, emitted only
for `todo_list`) and 9 item types. Parser traps to respect: `web_search`
serialises `id` twice, `declined` patches are folded into `failed`, and `error`
events carry no `will_retry`, so retry noise is shape-identical to a fatal error.

**A Teardown trap for ticket 06/07: rollout files are zstd-compressed when
cold.** A `*.jsonl` glob at Teardown would silently miss them. Directories are
local-time while line timestamps are UTC, and `codex exec` writes
`history_mode: paginated`, whose `TurnItem` tags are PascalCase while everything
else is snake_case.
