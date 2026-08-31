# Headless `claude` and `codex`: what the CLIs actually give us

Research for ticket `.scratch/v1-spec/issues/01-headless-claude-and-codex.md`.

Versions under test (both installed on this machine, both exercised directly —
every "observed" claim below came from running the binary, not from a doc):

- `claude` **2.1.251** (Claude Code), Homebrew cask `claude-code@latest`
- `codex` **codex-cli 0.151.0**, Homebrew cask `codex`

Date: 2026-08-31.

---

## Summary: what changed about our assumptions

1. **`turns` does not pass through to a CLI flag for both.** `claude` has
   `--max-turns` (hidden from `--help` in 2.1.251 but present and working, and
   still documented in the CLI reference). **`codex exec` has no turn, step, or
   tool-call cap** — confirmed by `--help`, by `strings`, and by a repo-wide
   grep of the source at tag `rust-v0.151.0`. The charting decision "turns pass
   through to the CLI's own flag" holds for `claude` only.
   See [Limit flags](#3-limit-flags).

2. **Both CLIs do have a *budget* limit, and it's the better abstraction.**
   `claude`: `--max-budget-usd` (dollars). `codex`: `-c
   features.rollout_budget.enabled=true -c features.rollout_budget.limit_tokens=N`
   (weighted tokens) — verified terminating a run mid-task. Neither is a turn
   count. Recommend the Agent YAML express limits as an optional per-CLI block,
   not a single `turns:` field. Caveat: codex's is `Stage::UnderDevelopment`
   and off by default.

3. **`claude --bare` and Skills do not mix.** Observed: `--bare` sees **zero**
   skills — not from `~/.claude/skills/`, and not from the cwd's own
   `.claude/skills/` either. Invoking one returns `Unknown command` with
   `is_error: false` and `subtype: success` — a silent no-op that looks like a
   successful Run. Since charting settled on `skills add` + a shared base image,
   the container must either **not** use `--bare`, or install skills
   project-scoped and pass `--add-dir <workspace>`. Note the docs call `--bare`
   *"the recommended mode for scripted and SDK calls"* and say it *"will become
   the default for `-p` in a future release"*. See [Skills](#7-skills-on-disk).

4. **`skills add -g -a codex` never writes to `~/.codex/skills`, and that is
   fine only by luck.** Verified in the `skills` 1.5.23 source and by running
   it: codex is a "universal agent", so its skills go to `~/.agents/skills/`
   and the agent-specific global dir is skipped. Independently measured:
   `codex` 0.151.0 *does* read `$HOME/.agents/skills` (skill root `r2`), so it
   works. `claude` reads only `~/.claude/skills` and `<project>/.claude/skills`
   — never `.agents/skills`. See [Skills](#7-skills-on-disk).

5. **`codex` does not read `OPENAI_API_KEY`.** Observed: with a clean
   `CODEX_HOME` and only `OPENAI_API_KEY` set, codex sends no credential at all
   (`401 ... Missing bearer or basic authentication in header`). With
   `CODEX_API_KEY` set, it sends the key (`401 ... auth error code:
   invalid_api_key`). The secrets path must deliver `CODEX_API_KEY`, not
   `OPENAI_API_KEY`. The public docs page mentions both; for 0.151.0 only one
   of them works.

6. **Exit codes are nearly useless for classification on their own.** Both CLIs
   return `1` for "auth failed", "hit the turn cap", "hit the budget cap", and
   "model error". Both return `143` on SIGTERM. Classification has to come from
   the structured stream, not `$?`. Worse: `claude` reports auth failure as
   `subtype: "success"` with `is_error: true` and
   `terminal_reason: "api_error"`. See [Exit codes](#4-exit-codes).

7. **Both structured streams are complete enough for a Journal, but neither is
   the richest artifact on disk.** `codex`'s on-disk rollout JSONL is
   substantially richer than its `--json` stream (64 KB vs 1 KB for the same
   trivial run) and includes the reasoning items and the full system prompt;
   `claude`'s `--output-format stream-json` stdout is *richer* than its on-disk
   transcript in one important way (it carries the terminal `result` event with
   cost and `terminal_reason`, which the transcript does not). So: capture
   **stdout** for `claude`, and consider capturing **stdout + the rollout file**
   for `codex`.

8. **`claude` can eat the whole `docker stop --time=30` grace by itself.** It
   waits up to 30 s to drain stdout before exiting. If the control plane reads
   the stream over a pipe, teardown may never get to push. See
   [Signals and Teardown](#signals-and-teardown-bears-on-the-container-pushes-at-teardown-decision).

9. **"Personality" resolves cheaply, no new machinery.** `claude` has
   `--append-system-prompt[-file]`; `codex` has `-c developer_instructions=`
   plus an auto-discovered workspace `AGENTS.md`. map.md can close that
   not-yet-specified item inside the schema ticket. See
   [Bonus: personality](#bonus-personality-has-a-concrete-answer-for-both).

---

## Divergences at a glance

What the Agent YAML has to abstract over. Rows marked ⚠ are where a single
shared key would be a lie.

| | `claude` 2.1.251 | `codex` 0.151.0 |
| --- | --- | --- |
| Non-interactive invocation | `claude -p "<prompt>"` (flag) | `codex exec "<prompt>"` (subcommand) |
| Prompt from file | none (argv or stdin only) | none (argv or stdin only) |
| Prompt from stdin | pipe, ≤ 10 MB | pipe, or literal `-` |
| Stdin left open | fine | ⚠ **hangs** — needs `< /dev/null` |
| Structured stream | `--output-format stream-json --verbose` | `--json` |
| Terminal event | `result` (subtype, cost, `terminal_reason`, `num_turns`) | ⚠ `turn.completed` / `turn.failed` (tokens only, no cost) |
| Final message to a file | no | `-o <FILE>` |
| System prompt override | `--system-prompt[-file]`, `--append-system-prompt[-file]` | `-c developer_instructions="..."`, or `AGENTS.md` in the workspace |
| Turn cap | `--max-turns <n>` (hidden but works) | ⚠ **none** |
| Budget cap | `--max-budget-usd <amt>` (dollars) | ⚠ `-c features.rollout_budget.*` (weighted tokens, unstable, off by default) |
| Wall-clock cap | none | none — and a network partition retries **forever** |
| Cost reported | ✅ `total_cost_usd` + per-model | ⚠ tokens only |
| Auth env var | `ANTHROPIC_API_KEY` | ⚠ `CODEX_API_KEY` (**not** `OPENAI_API_KEY`) |
| State dir override | `CLAUDE_CONFIG_DIR` | `CODEX_HOME` (+ `CODEX_SQLITE_HOME`) |
| Ephemeral state | `--no-session-persistence` | `--ephemeral` — no rollout recorder at all (SQLite state still written) |
| Exit on SIGINT | (turn ends, session resumable) | ⚠ **1**, and the stream stops with **no terminal event** |
| Skills: personal | `~/.claude/skills` | `$CODEX_HOME/skills`, `$HOME/.agents/skills` |
| Skills: project | `<cwd..root>/.claude/skills` | `<cwd>/.codex/skills`, `<cwd>/.agents/skills` |
| Reduced/CI mode | `--bare` — ⚠ disables skill discovery | `--ignore-user-config` — does not affect skills |
| Exit on SIGTERM | 143 | 143 |
| Exit on bad argv | 1 | ⚠ 2 |
| Sandbox of its own | permission modes | ⚠ additionally an OS sandbox (`-s`), needs `--dangerously-bypass-approvals-and-sandbox` inside Docker |
| Requires a git repo | no | ⚠ yes, unless `--skip-git-repo-check` |

---

## 1. Invoking a single non-interactive run, and how the prompt is passed

### `claude` 2.1.251

```
claude -p "<prompt>" [flags]
```

`-p` / `--print` is the non-interactive switch: *"Print response and exit
(useful for pipes)"* (`claude --help`). The prompt is a positional argument, or
stdin:

```
cat build-error.txt | claude -p 'concisely explain the root cause'
```

Both at once works: the piped stdin becomes context, the argument the
instruction. Piped stdin is **capped at 10 MB**; over the cap, `claude` exits
non-zero with an error (docs, `code.claude.com/docs/en/headless`).

There is **no flag that reads the user prompt from a file.** There are
`--system-prompt-file <file>` and `--append-system-prompt-file <file>` (both
present in 2.1.251, both hidden from `--help`, both confirmed in the binary's
option table) — these are the natural home for the Agent's "personality".

**Gotcha (observed, cost me a run):** several flags are variadic
(`--allowedTools <tools...>`, `--add-dir <directories...>`, `--tools <tools...>`,
`--mcp-config <configs...>`). Putting the prompt *after* one of them makes the
flag swallow it:

```
$ claude -p --output-format stream-json --allowedTools "Bash,Write" "Create a file..."
Error: Input must be provided either through stdin or as a prompt argument when using --print
```

The control plane must emit the prompt **immediately after `-p`**, or pipe it on
stdin. Piping on stdin is the safer construction for generated argv.

**Gotcha (observed):** `--output-format stream-json` errors out without
`--verbose`:

```
$ claude -p "hi" --output-format stream-json
Error: When using --print, --output-format=stream-json requires --verbose
```

### `codex` 0.151.0

```
codex exec [OPTIONS] "<prompt>"
```

`codex exec` is a first-class subcommand ("Run Codex non-interactively",
alias `e`). From `codex exec --help`:

> `[PROMPT]` — Initial instructions for the agent. If not provided as an
> argument (or if `-` is used), instructions are read from stdin. If stdin is
> piped and a prompt is also provided, stdin is appended as a `<stdin>` block

**Gotcha (observed):** if stdin is a pipe rather than a TTY, `codex exec` prints
`Reading additional input from stdin...` to stderr and waits for EOF. In a
container where stdin is an inherited-but-never-closed pipe this would hang
forever. **The entrypoint must redirect `< /dev/null`** (or close stdin) unless
it is deliberately piping the prompt.

Flags the container will want on every run:

| Flag | Why |
| --- | --- |
| `--skip-git-repo-check` | codex refuses to run outside a git repo without it |
| `-C, --cd <DIR>` | set the working root explicitly |
| `-s, --sandbox <read-only\|workspace-write\|danger-full-access>` | codex has its own sandbox on top of Docker |
| `--dangerously-bypass-approvals-and-sandbox` | *"Intended solely for running in environments that are externally sandboxed"* — i.e. exactly our container |
| `--json` | JSONL event stream on stdout |
| `-o, --output-last-message <FILE>` | writes the final assistant message to a file — cheap way to get "what it concluded" for the Journal |
| `--ephemeral` | *"Run without persisting session files to disk"* |
| `--ignore-user-config` | *"Do not load `$CODEX_HOME/config.toml`; auth still uses `CODEX_HOME`"* |
| `--output-schema <FILE>` | JSON Schema for the final response |

Note `-a/--ask-for-approval` exists on the **top-level** `codex` command but not
on `codex exec` (observed: `codex exec -a never` → `error: unexpected argument
'-a' found`). `codex exec` is non-interactive by construction.

**Divergence:** `claude` has one binary and a flag; `codex` has a subcommand.
The Agent YAML's runner shim has to build two structurally different argv
shapes. Nothing hard, but it is not "same flags, different binary".

---

## 2. The structured event stream (the Journal's raw material)

### `claude`: `--output-format stream-json --verbose`

Newline-delimited JSON on stdout. Observed event types from a real tool-using
run (`claude -p "Create a file test.txt ... then run 'cat test.txt' via Bash"`):

```
system    | hook_started      (x4)
system    | hook_response     (x4)
system    | init
system    | thinking_tokens   (x3)
assistant | (content: thinking)
assistant | (content: tool_use:Bash)
rate_limit_event
user      | (content: tool_result)
assistant | (content: text)
result    | success
```

Real shapes, verbatim from that run (elided where long):

`system/init` — first substantive event, carries the session id, model, resolved
tool list, MCP servers, plugins, and the discovered slash commands:

```json
{"type":"system","subtype":"init","cwd":"/private/tmp/ccprobe",
 "session_id":"eda61bcf-40ca-4dca-afc7-fe973bb02ef0",
 "tools":["Task","Bash","Edit","Read","Skill","WebFetch","Write", ...],
 "mcp_servers":[],"model":"claude-opus-5[1m]",
 "permissionMode":"bypassPermissions",
 "slash_commands":["code-review","init","mcp", ...]}
```

`assistant` with a `tool_use` block — note `parent_tool_use_id` (null for the
main conversation, set for subagents) and `request_id`:

```json
{"type":"assistant","message":{"model":"claude-opus-5","id":"msg_011Ceb...",
  "type":"message","role":"assistant",
  "content":[{"type":"tool_use","id":"toolu_01A3Uti...","name":"Bash",
    "input":{"command":"echo ok > test.txt && cat test.txt",
             "description":"Create test.txt and print it"},
    "caller":{"type":"direct"}}],
  "stop_reason":null,
  "usage":{"input_tokens":2,"cache_creation_input_tokens":13617,
           "cache_read_input_tokens":12859,"output_tokens":2,
           "service_tier":"standard"}},
 "parent_tool_use_id":null,
 "session_id":"eda61bcf-...","uuid":"47932f22-...",
 "timestamp":"2026-08-31T18:44:23.708Z","request_id":"req_011CebKYX1..."}
```

`user` with the `tool_result` — the raw stdout/stderr of the tool is in
`tool_use_result`:

```json
{"type":"user","message":{"role":"user",
  "content":[{"tool_use_id":"toolu_01A3Uti...","type":"tool_result",
              "content":"ok","is_error":false}]},
 "parent_tool_use_id":null,"session_id":"eda61bcf-...",
 "uuid":"46521136-...","timestamp":"2026-08-31T18:44:24.089Z",
 "tool_use_result":{"stdout":"ok","stderr":"","interrupted":false,
                    "isImage":false,"noOutputExpected":false}}
```

`rate_limit_event` — undocumented on the headless page but observed; useful for
the Journal because it tells you how close the account is to its window:

```json
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed",
  "resetsAt":1788208800,"rateLimitType":"five_hour",
  "unifiedWindows":{"five_hour":{"utilization":0.31,"resetsAt":1788208800},
                    "seven_day":{"utilization":0.53,"resetsAt":1788447600}}},
 "uuid":"06a6c3a4-...","session_id":"eda61bcf-..."}
```

`result` — the terminal event, and the single most valuable record for the
Journal. Observed keys:

```
api_error_status, duration_api_ms, duration_ms, fast_mode_disabled_reason,
fast_mode_state, is_error, modelUsage, num_turns, permission_denials,
queued_turn_count, result, session_id, stop_reason, subagent_stats,
subtype, terminal_reason, total_cost_usd, type, usage, uuid
```

A successful one:

```json
{"type":"result","subtype":"success","is_error":false,"num_turns":1,
 "stop_reason":"end_turn","terminal_reason":"completed",
 "total_cost_usd":0.1699985,
 "usage":{"input_tokens":2,"cache_creation_input_tokens":16395,
          "cache_read_input_tokens":10027,"output_tokens":41, ...},
 "modelUsage":{"claude-opus-5[1m]":{"inputTokens":2,"outputTokens":41,
   "costUSD":0.1699985,"contextWindow":1000000,"provider":"firstParty"}},
 "permission_denials":[],
 "subagent_stats":{"spawned":0, ...},
 "result":"...final assistant text...",
 "session_id":"...","duration_ms":2398,"uuid":"..."}
```

The `subtype` enum (extracted from the 2.1.251 binary's zod schema) is:

```
"success" | "error_during_execution" | "error_max_turns"
         | "error_max_budget_usd" | "error_max_structured_output_retries"
```

**Is it complete enough to reconstruct the Run?** Yes, with three caveats:

- Thinking blocks appear as `assistant` content of type `thinking` — present.
- Subagent (Task tool) *text* is **not** forwarded by default. Only their
  `tool_use`/`tool_result` blocks are. Pass `--forward-subagent-text` (or
  `CLAUDE_CODE_FORWARD_SUBAGENT_TEXT`) to reconstruct subagent transcripts;
  requires ≥ 2.1.211, and nested-subagent forwarding requires ≥ 2.1.219.
- Hook lifecycle events need `--include-hook-events`. Token-level deltas need
  `--include-partial-messages` (almost certainly not wanted for a Journal).

The **system prompt is never in the stream.** If the Journal needs to record
what the Agent was actually told, the control plane has to record its own
`--system-prompt-file` / `--append-system-prompt-file` content.

### `codex exec --json`

Also newline-delimited JSON on stdout. Observed from a real tool-using run
(`codex exec --json -s workspace-write "Create hello.txt ... then cat it"`), in
full — this is the whole stream for that run, which shows how much terser it is
than Claude's:

```json
{"type":"thread.started","thread_id":"01a05921-0893-77c3-b394-899ea5fc089e"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"I'll create `hello.txt` and verify its contents."}}
{"type":"item.started","item":{"id":"item_1","type":"file_change","changes":[{"path":"/tmp/cxprobe/hello.txt","kind":"add"}],"status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_1","type":"file_change","changes":[{"path":"/tmp/cxprobe/hello.txt","kind":"add"}],"status":"completed"}}
{"type":"item.started","item":{"id":"item_2","type":"command_execution","command":"/bin/zsh -lc 'cat hello.txt'","aggregated_output":"","exit_code":null,"status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_2","type":"command_execution","command":"/bin/zsh -lc 'cat hello.txt'","aggregated_output":"hello\n","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"Done. `hello.txt` contains `hello`, verified with `cat hello.txt`."}}
{"type":"turn.completed","usage":{"input_tokens":34172,"cached_input_tokens":27136,"cache_write_input_tokens":0,"output_tokens":163,"reasoning_output_tokens":26}}
```

Failures are also in the stream. Observed with a bad credential:

```json
{"type":"error","message":"Reconnecting... 2/5 (unexpected status 401 Unauthorized: Missing bearer or basic authentication in header, url: wss://api.openai.com/v1/responses, cf-ray: ...)"}
```

Note `codex` retries the connection **5 times with backoff** before giving up,
emitting a top-level `error` event per attempt. A misconfigured container burns
~20s before exiting.

`turn.failed` **does exist** in 0.151.0. Observed by forcing a bad model
(`codex exec -m definitely-not-a-real-model-xyz`), the complete stream:

```json
{"type":"thread.started","thread_id":"01a0592f-c91c-79e1-894e-92516e2f8c7a"}
{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Model metadata for `definitely-not-a-real-model-xyz` not found. Defaulting to fallback metadata; this can degrade performance and cause issues."}}
{"type":"turn.started"}
{"type":"error","message":"{\"type\":\"error\",\"status\":400,\"error\":{\"type\":\"invalid_request_error\",\"message\":\"The 'definitely-not-a-real-model-xyz' model is not supported when using Codex with a ChatGPT account.\"}}"}
{"type":"turn.failed","error":{"message":"{\"type\":\"error\",\"status\":400,...}"}}
```

Exit code 1. Note there are **two distinct error carriers**: a top-level
`{"type":"error","message":...}` event, and an `item` whose `type` is `"error"`
(warning-level, non-fatal — the run continued past it). A parser must handle
both.

**Complete schema**, from `codex-rs/exec/src/exec_events.rs` at tag
`rust-v0.151.0` (internally tagged, `#[serde(tag = "type")]`, one `println!` per
event). There are exactly **8** event types and **9** item types:

| event `type` | fields |
| --- | --- |
| `thread.started` | `thread_id: String` |
| `turn.started` | *(none — emits `{"type":"turn.started"}`)* |
| `turn.completed` | `usage: {input_tokens, cached_input_tokens, cache_write_input_tokens, output_tokens, reasoning_output_tokens}` |
| `turn.failed` | `error: {message: String}` |
| `item.started` | `item: ThreadItem` |
| `item.updated` | `item: ThreadItem` — **only ever emitted for `todo_list`** |
| `item.completed` | `item: ThreadItem` |
| `error` | `message: String` |

| item `type` | fields |
| --- | --- |
| `agent_message` | `text` |
| `reasoning` | `text` (summary only — see below) |
| `command_execution` | `command`, `aggregated_output`, `exit_code` (nullable), `status: in_progress\|completed\|failed\|declined` |
| `file_change` | `changes: [{path, kind: add\|delete\|update}]`, `status` |
| `mcp_tool_call` | `server`, `tool`, `arguments`, `result`, `error`, `status` |
| `collab_tool_call` | `tool`, `sender_thread_id`, `receiver_thread_ids`, `prompt`, `agents_states`, `status` |
| `web_search` | `id`, `query`, `action` |
| `todo_list` | `items: [{text, completed}]` |
| `error` | `message` |

Every top-level field is always present — no `Option`, no
`skip_serializing_if` at the event level. `ThreadItem.id` is a synthetic
counter (`item_0`, `item_1`, …), **not** a model-assigned id.

Parser gotchas, all from source:

- `agent_message` and `reasoning` **never** get an `item.started` — completed-only.
- `web_search` serialises `id` **twice** (outer `ThreadItem.id` then the
  flattened inner `WebSearchItem.id`). Last-wins is parser-dependent.
- A **declined** patch is folded into `status: "failed"` — you cannot tell a
  denied edit from a broken one.
- `error` events carry **no `will_retry` flag**, so the five `"Reconnecting...
  3/5"` lines are shape-identical to a fatal error. **Only `turn.failed` is
  terminal.**
- The published TypeScript SDK mirror (`sdk/typescript/src/items.ts`) is stale —
  missing `declined`, `collab_tool_call`, and `WebSearchItem.action`. Trust
  `exec_events.rs`.

**Structured error codes exist internally and are thrown away.**
`app-server-protocol/.../shared.rs` defines `CodexErrorInfo` with machine-readable
variants — `SessionBudgetExceeded`, `UsageLimitExceeded`, `RateLimitExceeded`,
`ContextWindowExceeded`, `Unauthorized`, `SandboxError`, `ServerOverloaded`,
`InternalServerError`. The `exec` mapper explicitly drops `codex_error_info` and
flattens everything to `ThreadErrorEvent { message: String }`. **If we ever want
structured limit-vs-crash discrimination from codex, `codex exec --json` cannot
give it — that needs `codex app-server` (JSON-RPC) instead.** Out of scope for
v1; worth knowing the ceiling.

**What is missing versus Claude's stream:**

- **`turn.completed` carries no stop reason and no success flag** — just
  `usage`. The success/failure signal is which terminal event you got
  (`turn.completed` vs `turn.failed`), not a field.
- **No cost.** Only token counts. Claude gives `total_cost_usd` and a per-model
  breakdown. (This bears directly on map.md's open item "Cost and token
  accounting per Run": free for `claude`, tokens-only for `codex`.)
- **Reasoning is summary-only, and silently omitted when the summary is empty.**
  My run reported `reasoning_output_tokens: 26` and emitted no `reasoning` item
  at all. The JSONL gate is hardcoded to summaries and suppresses empty ones;
  `show_raw_agent_reasoning` has **zero effect** on `--json` (it only touches
  the human-output processor). The raw/encrypted chain of thought is in the
  rollout file (`response_item.reasoning.encrypted_content`, ~3 KB in a sampled
  run) and nowhere else.
- **No session/init event** listing tools, model, or skills. `thread.started`
  carries only `thread_id`.

**Codex's on-disk rollout file is the richer artifact.** For the same trivial
run, the stream was 9 lines / ~1 KB and the rollout file was 23 lines / 64 KB.
Rollout path: `$CODEX_HOME/sessions/YYYY/MM/DD/rollout-<ISO8601>-<thread_id>.jsonl`.
Observed record types for that run:

```
session_meta       x1   (session_id, cwd, originator:"codex_exec", cli_version,
                         source:"exec", model_provider, AND base_instructions —
                         the entire system prompt, verbatim)
turn_context       x1
world_state        x1
event_msg/task_started      x1
event_msg/item_completed    x6
event_msg/token_count       x2
event_msg/task_complete     x1
response_item/message (developer x3, user x2, assistant x2)
response_item/reasoning     x1
response_item/custom_tool_call        x1
response_item/custom_tool_call_output x1
```

Each record has `timestamp`, `ordinal`, `type`, `payload`. **The rollout file
survives SIGTERM** (observed: killed mid-`sleep`, 57 KB file present and
well-formed afterwards).

**Recommendation for the Journal:** capture stdout for both. Additionally
capture `$CODEX_HOME/sessions/**/rollout-*.jsonl` for codex Runs, or accept a
materially thinner record than for claude Runs. Do **not** rely on Claude's
on-disk transcript (`$CLAUDE_CONFIG_DIR/projects/<slug>/<session>.jsonl`) — it
lacks the `result` event and the `system/init` event entirely (observed record
types: `assistant`, `user`, `attachment`, `last-prompt`, `queue-operation`,
`atis-latch`).

**Divergence summary:** the two streams share no field names, no event names,
and no envelope. Anything that wants "one Journal format" has to be a
translation layer, not a passthrough. The cheapest honest v1 is: store both raw
streams verbatim, plus a small normalised header the control plane writes
itself (run id, agent, exit code, wall time, and — for claude only — cost).

---

## 3. Limit flags

| Limit | `claude` 2.1.251 | `codex` 0.151.0 |
| --- | --- | --- |
| Turns / steps | **`--max-turns <n>`** — present and working, but `.hideHelp()` in 2.1.251, so it does *not* appear in `claude --help`. Still in the docs. | **none** |
| Dollars | **`--max-budget-usd <amount>`** | none |
| Tokens | `--max-thinking-tokens` (deprecated), `--task-budget <tokens>` ("API-side task budget in tokens", hidden, undocumented) | none that terminates a run |
| Wall clock | none | none |
| Context | `--autocompact <auto\|tokens>` | `model_auto_compact_token_limit` (config) — compacts, does not terminate |

### `claude --max-turns`

Extracted verbatim from the 2.1.251 binary's option table:

```js
.addOption(new U("--max-turns <turns>",
  "Maximum number of agentic turns in non-interactive mode. This will early "
  + "exit the conversation after the specified number of turns. "
  + "(only works with --print)").argParser(Nn).hideHelp())
```

Observed behaviour with `--max-turns 1` on a multi-step task:

```
exit code 1
{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":2,
 "stop_reason":"tool_use","terminal_reason":"max_turns",
 "errors":["Reached maximum number of turns (1)"],"result":null}
```

Note `num_turns: 2` for `--max-turns 1` — the counter and the cap are off by
one. Do not build accounting on `num_turns == max_turns`.

**Risk to record:** the flag being `hideHelp()`'d in 2.1.251 is a soft signal
that it may be de-emphasised. `--max-budget-usd` is visible in `--help` and is
the flag Anthropic documents most prominently for cost control.

### `claude --max-budget-usd`

Observed with `--max-budget-usd 0.0001` on a one-turn poem:

```
exit code 1
{"type":"result","subtype":"error_max_budget_usd","is_error":true,"num_turns":1,
 "stop_reason":"end_turn","terminal_reason":"budget_exhausted",
 "errors":["Reached maximum budget ($0.0001)"],
 "total_cost_usd":0.035320000000000004}
```

**The cap is checked after a turn completes, not before.** Actual spend was
$0.0353 against a $0.0001 cap — a 350x overshoot. It is a stop condition, not a
guarantee. Size it accordingly.

### `codex`: no turn cap — but there *is* a token budget that hard-stops a run

**No turn / step / round-trip / tool-call cap exists.** Confirmed four ways:

1. `codex exec --help` for 0.151.0 lists no such flag — the full option list is
   `exec/src/cli.rs:10-81` plus `utils/cli/src/shared_options.rs:10-73`, and is
   reproduced in §1 above.
2. `strings` on the 0.151.0 binary: no `max_turns`, `max_steps`,
   `max_iterations`, `tool_call_limit`.
3. Repo-wide grep at tag `rust-v0.151.0` for
   `max_turns|max_steps|max_iterations|tool_call_limit|max_tool_calls|turn_limit|step_limit|max_requests`
   returns only `tui/src/dynamic_tools.rs:91` — an unrelated "read N turns of
   history" TUI argument.
4. The official non-interactive docs page: *"No explicit turn/iteration limits
   documented."*

**Correction to something I would otherwise have got wrong:**
`agents.job_max_runtime_seconds` is a **dead key** — `config/src/config_toml.rs:688-690`
calls it *"Removed agent-job setting retained as a no-op for compatibility"*,
and the test that pins the behaviour is literally named
`legacy_agent_job_max_runtime_seconds_is_accepted_as_noop`. It parses and does
nothing. Do not plan around it.

#### `features.rollout_budget` — the one knob that will stop a run

This is a weighted-**token** budget, not a step cap, but it does terminate a
run mid-task. Defined in `features/src/feature_configs.rs:333-350`, registered
`features/src/lib.rs:1490-1495` (`stage: UnderDevelopment, default_enabled:
false`), enforced `core/src/rollout_budget.rs:46-65`:

```rust
usage.output_tokens.max(0) as f64 * state.config.sampling_token_weight
    + usage.non_cached_input() as f64 * state.config.prefill_token_weight
...
state.weighted_tokens_used >= state.config.limit_tokens as f64
```

Verified working end to end:

```
codex exec --skip-git-repo-check --json \
  -c 'features.rollout_budget.enabled=true' \
  -c 'features.rollout_budget.limit_tokens=4000' \
  -c 'features.rollout_budget.reminder_at_remaining_tokens=[1000]' \
  "Say ok, then run 'echo one', then 'echo two', then 'echo three', then say done."
```

It ran `echo one` and `echo two`, then:

```json
{"type":"error","message":"shared rollout token budget exhausted"}
{"type":"turn.failed","error":{"message":"shared rollout token budget exhausted"}}
```

exit code 1. Caveats, all load-bearing:

- `limit_tokens` **and** `reminder_at_remaining_tokens` are both mandatory when
  enabled; each reminder must be `> 0` and `< limit_tokens` or config load fails
  (`core/src/config/mod.rs:2801-2841`).
- Weighted tokens, both weights default `1.0`. It is a cost cap, not a step cap.
- **Shared across the session tree** — subagents draw from the same pool.
- `Stage::UnderDevelopment`. Enabling it injects a warning item into the `--json`
  stream (suppress with `suppress_unstable_features_warning = true`), and it can
  change or vanish between releases.
- The model is *told* about the budget: reminders are injected as
  `<rollout_budget>You have N weighted tokens left…</rollout_budget>`.
- The exit code is a plain `1` — indistinguishable from a crash without string-
  matching `"shared rollout token budget exhausted"` (which is a `Display` impl
  at `protocol/src/error.rs:86`, not a stable API).

#### Other mechanisms — none of which bound a run

| Mechanism | Bounds the run? |
| --- | --- |
| `model_auto_compact_token_limit` (+ `_scope`) | **No** — *extends* runs by compacting |
| `model_context_window` | No — overflow triggers a compaction retry loop that drops oldest items |
| `features.token_budget` | **No** — reminder text and fallback prompts only |
| `agents.max_concurrent_threads_per_session`, `agents.max_depth` | Caps parallel/nested subagents, not the parent's length |
| per-command `SandboxErr::Timeout` | Bounds one tool call |
| `agents.job_max_runtime_seconds` | **No-op** (see above) |

**What to do — three options, in order of laziness:**

1. **Rely on the container time kill that map.md already specifies.** This is
   the recommendation. It costs zero code, and it is *mandatory anyway*: a
   codex run against an unreachable endpoint retries **forever**
   (`{"type":"error","message":"Reconnecting... waiting for network"}` in a
   loop; `ConnectionFailed` is classified retryable at
   `protocol/src/error.rs:403`). A 401 gives up after 5 attempts, but a network
   partition does not. **Any caller must impose its own wall clock.**
2. **`-c features.rollout_budget.*`** if a token ceiling per Run is wanted. It
   works today and is the closest analogue to claude's `--max-budget-usd`. Flag
   it as an unstable dependency.
3. **Don't model turns in the Agent YAML at all.** Model `timeout` (universal)
   plus an optional per-CLI limits block: `max_turns` / `max_budget_usd` for
   claude, `rollout_budget_tokens` for codex. A shared `turns:` key that
   silently does nothing for half the Agents is worse than no key.

A `PreToolUse` hook that counts and denies is *possible* (codex 0.151.0 supports
hooks at `$CODEX_HOME/hooks.json` or `<repo>/.codex/hooks.json`; a `PreToolUse`
hook denies with
`{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"..."}}`
or exit 2 + stderr) but it is the worst option: confirmed from the binary,
`PreToolUse hook returned unsupported continue:false` — the hook **cannot
terminate the run**, only refuse calls, and it does not bound pure-reasoning
turns. It also needs `--dangerously-bypass-hook-trust`. Skip it.

**map.md correction needed:** *"Limits use what already exists (charting): time
is a container kill via a context deadline; turns pass through to the CLI's own
flag. No custom accounting."* — the second clause is true for `claude` only. For
`codex` the honest statement is "time is the only bound, optionally plus an
unstable token budget".

---

## 4. Exit codes

All observed on this machine, both binaries.

| Condition | `claude` 2.1.251 | `codex` 0.151.0 |
| --- | --- | --- |
| Success | `0` | `0` |
| Unknown/invalid CLI flag | `1` (`error: unknown option '--bogus-flag'` on stderr, no JSON) | `2` (`error: unexpected argument '--bogus-flag' found` on stderr, no JSON) |
| Not authenticated | `1` | `1` |
| Hit `--max-turns` | `1` | n/a |
| Hit `--max-budget-usd` | `1` | n/a |
| SIGTERM | `143` (documented, and consistent with 128+15) | `143` (observed) |

**A caller cannot distinguish "finished" from "hit its limit" from "crashed" by
exit code.** Everything that is not a clean finish, a signal, or an argv error
collapses to `1`.

For `claude`, the discrimination is available in the terminal `result` event:

| Meaning | `subtype` | `terminal_reason` | `is_error` | exit |
| --- | --- | --- | --- | --- |
| finished | `success` | `completed` | `false` | 0 |
| hit turn cap | `error_max_turns` | `max_turns` | `true` | 1 |
| hit budget cap | `error_max_budget_usd` | `budget_exhausted` | `true` | 1 |
| API/auth failure | **`success`** | `api_error` | `true` | 1 |
| execution error | `error_during_execution` | (varies) | `true` | 1 |

**Trap:** the auth-failure row. Observed verbatim with `--bare` and no
`ANTHROPIC_API_KEY`:

```json
{"type":"result","subtype":"success","is_error":true,"num_turns":1,
 "stop_reason":"stop_sequence","terminal_reason":"api_error",
 "result":"Not logged in · Please run /login","errors":null}
```

`subtype: "success"` on a run that did nothing. **The control plane must branch
on `is_error` and `terminal_reason`, never on `subtype` alone.**

**Second trap:** an unknown slash command in `-p` mode is a silent success.
Observed: `claude --bare -p "/p-homeclaude"` →
`{"subtype":"success","is_error":false,"terminal_reason":null,"result":"Unknown
command: /p-homeclaude"}`. If the Agent's prompt invokes a skill that failed to
install, the Run reports success and does nothing. Worth a Teardown-time
assertion.

For `codex`, the discrimination is *which terminal event you got*, not a field:

| Meaning | terminal event | exit |
| --- | --- | --- |
| finished | `turn.completed` (`usage` only) | 0 |
| failed (model / API / auth / budget) | `turn.failed` (`error.message`) | 1 |
| **sandbox denial** | `turn.completed` — the model works around it | **0** |
| killed at the time limit / teardown (SIGTERM) | none — stream truncates mid-item | 143 |
| SIGINT | **none — no terminal event at all** | 1 |
| our argv is wrong | none — empty stdout, `clap` error on stderr | 2 |
| panic | none | 101 (inferred, not triggered) |
| network partition | never — retries **forever** | never exits |

There is exactly one decision point in the source
(`exec/src/lib.rs:1037-1145`): a boolean `error_seen`, set by a non-retryable
top-level `error` notification or by a turn ending `Failed`/`Interrupted`, then
`if error_seen { std::process::exit(1) }`. Consequences:

- **`error_seen` is sticky.** A single non-retryable `error` event makes the
  process exit 1 **even if the turn subsequently completes successfully.**
- **`TurnStatus::Interrupted` emits no JSON.** SIGINT produces a stream that
  just stops, with no `turn.completed` and no `turn.failed`. Only the exit code
  (1) distinguishes it from a SIGTERM truncation (143). SIGTERM has **no
  handler anywhere on the `codex exec` path** — 143 is the kernel default.
- **Sandbox denials do not fail the run.** In exec mode all approval requests
  are auto-rejected; the denial surfaces as a failed `command_execution` item
  that the model reasons around, and the turn still completes → exit 0. If we
  care that a Run was blocked, we have to scan items, not `$?`.

Two error carriers to handle: top-level `{"type":"error","message":...}` (which
appears once per connection retry and does **not** by itself mean failure) and
`item.completed` with `item.type == "error"` (a warning; the run continues).
Only `turn.failed` is terminal.

"Hit its limit" is representable only by string-matching
`"shared rollout token budget exhausted"` on the `turn.failed` message, and only
if `features.rollout_budget` was enabled. There is no code and no field for it.

---

---

## Bonus: "personality" has a concrete answer for both

map.md lists *"What 'personality' concretely is"* as not-yet-specified. Both CLIs
support it without a mounted file, measured with `codex debug prompt-input`
(free — it renders the model-visible prompt without an API call):

| Mechanism | `claude` | `codex` |
| --- | --- | --- |
| Replace the system prompt | `--system-prompt <str>` / `--system-prompt-file <file>` | `-c developer_instructions="<str>"` |
| Append to it | `--append-system-prompt <str>` / `--append-system-prompt-file <file>` | (no append variant found) |
| Repo-level doc | `CLAUDE.md` (auto-discovered) | `AGENTS.md` (auto-discovered) |
| Canned tone | — | `-c personality=<none\|friendly\|pragmatic>` (enum, not free text) |

Observed: `codex -c developer_instructions="ZZDEVZZ do the thing"` becomes the
**first content block of the first `developer`-role message**, ahead of the
skills index. A workspace `AGENTS.md` is injected separately as a `user`-role
message, `"# AGENTS.md instructions for <path>\n\n<INSTRUCTIONS>\n...\n</INSTRUCTIONS>"`.
Related config keys exist for tuning that: `project_doc_max_bytes`,
`project_doc_fallback_filenames`.

So the Agent YAML can carry a single free-text `personality:` string and map it
to `--append-system-prompt` for claude and `-c developer_instructions=` for
codex. The semantics differ slightly (append vs replace) — worth one line in the
schema ticket, not a blocker.

---

## Signals and Teardown (bears on the "container pushes at teardown" decision)

Both CLIs exit **143** on SIGTERM, so a single `trap` in the entrypoint wrapper
handles both identically. Three timing facts that interact badly with
`docker stop --time=30`:

- **`claude` drains its stdout before exiting, waiting up to 30 s** (docs: *"If
  your consumer reads the stream slowly, Claude Code waits for the queued output
  to drain before exiting, scaling the wait with how much is still queued,
  capped at 30 seconds"*). If the control plane reads the stream slowly, claude
  can consume the entire `--time=30` grace on its own, leaving zero for the
  Journal push before SIGKILL. **Recommend `docker stop --time` be comfortably
  larger than 30 s, or that the wrapper redirect stdout to a file rather than a
  pipe the control plane reads.**
- **`claude` waits up to 10 minutes for background subagents** by default
  (`CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS`, ≥ 2.1.182). Background Bash tasks get
  a 5 s grace. Neither is bounded by `--max-turns`.
- **On SIGTERM `claude` runs `SessionEnd` hooks and kills the process tree of any
  running Bash command**, then exits. It starts no new tool call or model
  request. So the CLI's own shutdown is well-behaved; the risk is purely the
  30 s drain.
- **`codex`'s rollout file survives SIGTERM** (observed: killed mid-`sleep`,
  57 KB well-formed file present afterwards). Its `--json` stream simply
  truncates mid-`item.started`.

## 5. What each writes to disk during a run

### `claude`

Root is `$CLAUDE_CONFIG_DIR` if set, else `$HOME/.claude`. **`CLAUDE_CONFIG_DIR`
works** (observed: with it set, `projects/`, `sessions/`, `backups/` and
`.claude.json` were created under the new path).

Observed created by a single run in a pristine `HOME`:

```
~/.claude.json                                    (global state, plus backups/)
~/.claude/backups/.claude.json.backup.<epoch_ms>
~/.claude/projects/<slugified-cwd>/               (session transcripts)
~/.claude/sessions/
```

On a warm install, also: `shell-snapshots/` (one `snapshot-zsh-*.sh` per run),
`file-history/`, `session-env/`, `session-data/`, `history.jsonl`,
`statsig`/`stats-cache.json`, `paste-cache/`, and `.credentials.json` when
logged in with OAuth.

The session transcript is
`$CLAUDE_CONFIG_DIR/projects/<slug>/<session-uuid>.jsonl`. Record types observed:
`assistant`, `user`, `attachment`, `last-prompt`, `queue-operation`,
`atis-latch`. Each record carries `cwd`, `gitBranch`, `version`,
`permissionMode`, `sessionId`, `uuid`, `parentUuid`, `timestamp`. **No `result`
record.**

`--no-session-persistence` disables transcript writing entirely (print mode
only). Useful if we decide the stdout stream is the only record we want.

### `codex`

Root is `$CODEX_HOME` if set, else `$HOME/.codex`. Observed created by a single
run into a pristine `CODEX_HOME`:

```
installation_id
config.toml            (written back on first run)
sessions/YYYY/MM/DD/rollout-<ts>-<thread_id>.jsonl
shell_snapshots/
skills/                (with skills/.system/ extracted: imagegen, openai-docs,
                        plugin-creator, review-agent, skill-creator,
                        skill-installer)
thread-writer-locks/.coordination.lock
tmp/, .tmp/ (plugins sync)
.sandbox_migration
state_5.sqlite   + -wal + -shm
logs_2.sqlite    + -wal + -shm      (27 MB on this machine)
queue_1.sqlite   + -wal + -shm
goals_1.sqlite   + -wal + -shm
memories_1.sqlite+ -wal + -shm
thread_history_1.sqlite
models_cache.json
version.json
cache/, plugins/, rules/
```

**`codex` writes considerably more than `claude`, including six SQLite
databases.** `$CODEX_HOME` must be a writable, reasonably-sized path in the
container. `--ephemeral` suppresses session file persistence; it does not
suppress the SQLite state.

**Divergence:** `claude` has `CLAUDE_CONFIG_DIR`, `codex` has `CODEX_HOME`, and
codex additionally honours `CODEX_SQLITE_HOME`. Both are relocatable, so a
per-Run tmpfs for agent state is straightforward, but the codex one needs more
room.

---

## 6. Headless authentication, and what the secrets path must deliver

### `claude`

Precedence and mechanisms (docs `code.claude.com/docs/en/env-vars`, confirmed
present in the 2.1.251 binary):

| Variable | Docs description |
| --- | --- |
| `ANTHROPIC_API_KEY` | *"API key sent as `X-Api-Key` header. When set, this key is used instead of your Claude Pro, Max, Team, or Enterprise subscription even if you are logged in. In non-interactive mode (`-p`), the key is always used when present."* |
| `ANTHROPIC_AUTH_TOKEN` | *"Custom value for the `Authorization` header (the value you set here will be prefixed with `Bearer `)"* |
| `CLAUDE_CODE_OAUTH_TOKEN` | present in the binary (101 occurrences); the long-lived token produced by `claude setup-token` (*"Set up a long-lived authentication token (requires Claude subscription)"*, `claude --help`). Not listed on the env-vars docs page. |

The `--bare` flag's own help text is the clearest statement of the container
contract:

> Anthropic auth is strictly `ANTHROPIC_API_KEY` or `apiKeyHelper` via
> `--settings` (OAuth and keychain are never read).

So: **an API key is one secret, one env var, no interactive step.** If we want
to run on a Max subscription instead of metered API billing, the path is
`claude setup-token` **once, outside the container, interactively**, then ship
the resulting token as `CLAUDE_CODE_OAUTH_TOKEN` — and that token is not
compatible with `--bare`.

Also worth setting in the image: `DISABLE_AUTOUPDATER=1` (present in the
binary), `DISABLE_TELEMETRY`, `DISABLE_ERROR_REPORTING`,
`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` — a container that self-updates
mid-Run is a reproducibility hazard.

### `codex`

Three mechanisms, and **the obvious one does not work**:

1. **`CODEX_API_KEY` — works.** Observed with a pristine `CODEX_HOME` and a
   deliberately invalid key:
   `401 Unauthorized: {"error": {...}} ... auth error code: invalid_api_key`.
   The key was sent.
2. **`OPENAI_API_KEY` — does NOT work in 0.151.0.** Observed with a pristine
   `CODEX_HOME` and the same invalid key set as `OPENAI_API_KEY`:
   `401 Unauthorized: Missing bearer or basic authentication in header`. No
   credential was sent at all. (The string `OPENAI_API_KEY` does appear in the
   binary, so it is presumably read somewhere — for `codex login` or for a
   custom `model_provider` — but it does not authenticate a plain
   `codex exec`.) **This is the single most likely way to misconfigure the
   container.**
3. **`$CODEX_HOME/auth.json`** — the file `codex login` writes. Observed keys:
   `auth_mode`, `OPENAI_API_KEY`, `tokens`, `last_refresh`. `codex doctor`
   reports `auth storage mode: File`, `auth file: ~/.codex/auth.json`,
   `stored auth mode: chatgpt`. Headless population is supported:

   ```
   printenv OPENAI_API_KEY | codex login --with-api-key
   printenv CODEX_ACCESS_TOKEN | codex login --with-access-token
   ```

   (verbatim from `codex login --help`). `--with-access-token` is the
   ChatGPT-subscription path; the token comes from an interactive login
   elsewhere and expires.

**What the secrets path must deliver:**

| Agent kind | Secret name | Delivered as |
| --- | --- | --- |
| claude, metered API | Anthropic API key | env `ANTHROPIC_API_KEY` |
| claude, subscription | `setup-token` output | env `CLAUDE_CODE_OAUTH_TOKEN` (and no `--bare`) |
| codex, metered API | OpenAI API key | env **`CODEX_API_KEY`** (not `OPENAI_API_KEY`) |
| codex, subscription | ChatGPT access token | stdin to `codex login --with-access-token` at container start, or a pre-baked `auth.json` |
| both | git PAT | already decided: one fine-grained PAT for Journal + work push |

Note the subscription paths both involve a **refreshable/expiring** token, which
the Broker/allowlist model does not currently account for — an `age`-encrypted
ciphertext inline in the Agent YAML is a poor home for a credential that rotates
on its own. The API-key paths are static and fit the existing design cleanly.
**Recommend v1 uses API keys for both CLIs** and defers subscription auth.

---

## 7. Skills on disk

### Where each CLI actually looks — measured, not inferred

I built a controlled fixture: probe skills in six candidate locations, an
isolated `HOME`, an isolated `CODEX_HOME`, and an isolated project directory.

**`codex` 0.151.0.** `codex debug prompt-input` renders the exact model-visible
prompt, which contains a `### Skill roots` table. Verbatim output with
`HOME=/tmp/fh2 CODEX_HOME=/tmp/ch2`, cwd `/tmp/sk2`:

```
### Skill roots
- `r0` = `/private/tmp/sk2/.codex/skills`
- `r1` = `/private/tmp/ch2/skills`
- `r2` = `/private/tmp/fh2/.agents/skills`
- `r3` = `/private/tmp/ch2/skills/.system`
- `r4` = `/private/tmp/sk2/.agents/skills`
```

Generalised:

| Root | Path |
| --- | --- |
| r0 | `<cwd>/.codex/skills` |
| r1 | `$CODEX_HOME/skills` |
| r2 | `$HOME/.agents/skills` |
| r3 | `$CODEX_HOME/skills/.system` (bundled, auto-extracted) |
| r4 | `<cwd>/.agents/skills` |

All four non-bundled probes were listed in `### Available skills`. Skills are
injected as a **name + description + path index** into the system prompt
(progressive disclosure), not as full text.

**`claude` 2.1.251.** Measured via the `slash_commands` array in the
`system/init` stream event, same fixture:

| Setup | cwd | probes visible |
| --- | --- | --- |
| default | project | `p-homeclaude` (`~/.claude/skills`), `p-claudeproj` (`<project>/.claude/skills`) |
| `--bare` | project | **none** |
| `--bare --add-dir <project>` | `/tmp` | `p-claudeproj` only |

`claude` did **not** see `~/.agents/skills` or `<cwd>/.agents/skills` or
`.codex/skills`. Documented locations (docs `code.claude.com/docs/en/skills`):

| Location | Path |
| --- | --- |
| Enterprise | managed settings |
| Personal | `~/.claude/skills/<skill-name>/SKILL.md` |
| Project | `.claude/skills/<skill-name>/SKILL.md` (cwd and every parent up to repo root) |
| Plugin | `<plugin>/skills/<skill-name>/SKILL.md` |

Docs confirm symlinks are supported: *"A `<skill-name>` entry in the enterprise,
personal, or project locations can be a symlink to a directory elsewhere on
disk. Claude Code follows the symlink and reads `SKILL.md` from the target
directory."*

### Does that match where `skills add` puts them?

**Yes — but not the way the `skills` README says, and not the way it looks.**

`skills` CLI: npm package `skills`, **version 1.5.23** (repo HEAD == published
`latest`; `npx --yes skills@latest --version` → `1.5.23`; `engines.node
>=22.20.0`; deps `tar`, `yaml`).

The registry is `src/agents.ts`, 76 agents:

```ts
const codexHome  = process.env.CODEX_HOME?.trim()        || join(home, '.codex');
const claudeHome = process.env.CLAUDE_CONFIG_DIR?.trim() || join(home, '.claude');

'claude-code': { skillsDir: '.claude/skills',
                 globalSkillsDir: join(claudeHome, 'skills') },
codex:         { skillsDir: '.agents/skills',
                 globalSkillsDir: join(codexHome, 'skills') },
cursor:        { skillsDir: '.agents/skills',
                 globalSkillsDir: join(home, '.cursor/skills') },
```

Canonical store, `src/installer.ts`:

```ts
export function getCanonicalSkillsDir(global: boolean, cwd?: string): string {
  const baseDir = global ? homedir() : cwd || process.cwd();
  return join(baseDir, '.agents', 'skills');
}
```

`codex` (and `cursor`, and 17 others) is a **"universal agent"** —
`isUniversalAgent()` returns true for any agent whose `skillsDir` is
`.agents/skills`. For universal agents the CLI **ignores `globalSkillsDir`
entirely**:

```ts
// For universal agents with global install, the skill is already in the canonical
// ~/.agents/skills directory. Skip creating a symlink to the agent-specific global dir
if (isGlobal && isUniversalAgent(agentType)) {
  return { success: true, path: canonicalDir, canonicalPath: canonicalDir, mode: 'symlink' };
}
```

So **`skills add ... -g -a codex` never writes to `~/.codex/skills`** — verified
by running it in a clean `HOME`, which produced only
`$HOME/.agents/skills/<name>/SKILL.md` and no `$HOME/.codex/` at all. The
README's per-agent table (which lists `~/.codex/skills/` for Codex global) does
not describe the CLI's behaviour.

**This is fine for us, and only because of the r2 measurement above:** codex
0.151.0 reads `$HOME/.agents/skills` natively, so the skills land exactly where
codex looks. Had codex not had root r2, `-a codex -g` would install skills that
codex could never see.

**Second undocumented behaviour: symlink is not the default for a single
agent.** `src/add.ts`:

```ts
let installMode: InstallMode = options.copy ? 'copy' : 'symlink';
const uniqueDirs = new Set(targetAgents.map((a) => agents[a].skillsDir));
if (!options.copy && !options.yes && uniqueDirs.size > 1) {
  ... // interactive symlink/copy prompt
} else if (uniqueDirs.size <= 1) {
  // Single target directory — default to copy (no symlink needed)
  installMode = 'copy';
}
```

Measured in a clean `HOME` each time:

| command | result |
| --- | --- |
| `skills add <src> -g -a claude-code -y` | **copy** → real dir `~/.claude/skills/<name>/`; `~/.agents/skills` never created |
| `skills add <src> -g -a codex -y` | **copy** → real dir `~/.agents/skills/<name>/` |
| the two above, sequentially, same HOME | two independent real directories, no symlink |
| `skills add <src> -g -a claude-code codex -y` | `~/.agents/skills/<name>/` real + `~/.claude/skills/<name>` → relative symlink |

So the `~/.claude/skills/<name> -> ../../.agents/skills/<name>` layout observed
on this machine came from a **multi-agent** invocation. Two single-agent
invocations produce two copies. Both work; the multi-agent one is cheaper (one
clone, one lockfile, one copy on disk).

**Recommended container command — one invocation, both agents:**

```
npx --yes skills@1.5.23 add <source> -g -a claude-code codex -s '*' -y
```

Pin the version. The copy-vs-symlink downgrade and the universal-agent skip are
both undocumented and could move between releases.

Argument-order gotcha: `-a` and `-s` greedily consume following non-`-` tokens,
so the source must come **before** them. (Same class of bug as Claude's variadic
`--allowedTools`.)

Sources supported (`src/source-parser.ts`): local absolute/relative paths
(checked first), GitHub shorthand `owner/repo[/subpath]` and `owner/repo@skill`,
`github:`/`gitlab:` prefixes, `.../tree/<ref>/<path>` URLs, `#ref` fragments,
and a fallback that treats anything else as a direct git URL. **The container
needs `git` on `PATH`** — the no-clone blob fast path is allow-listed to
`['vercel','vercel-labs','heygen-com']`; everything else clones via `simple-git`
or `gh repo clone`.

Lockfiles: global installs write `~/.agents/.skill-lock.json` (`CURRENT_VERSION
= 3`, or `$XDG_STATE_HOME/skills/.skill-lock.json` if set) — matches the
`"version": 3` file observed here. Project installs write
`<cwd>/skills-lock.json` (`CURRENT_VERSION = 1`) instead.

Exit code **1** on failure (verified: nonexistent repo, missing local path,
invalid agent name), 0 on success. Caveats: a *cancelled prompt* exits **0**, and
"Installation failed" still prints a success-shaped outro — so the entrypoint
should check the exit code *and* assert the skill directories exist afterwards.

Writable paths the container needs for `skills add`: `~/.agents/`,
`~/.claude/skills/` (or `$CLAUDE_CONFIG_DIR/skills/`), `$TMPDIR` (`mkdtemp`
under `skills-*` / `skills-download-*`), `~/.npm/_npx`, and `~/.local/state/gh`
if the `gh` codepath is taken. Network: git/GitHub plus telemetry and a
security-audit API call — silence both with `DISABLE_TELEMETRY=1` or
`DO_NOT_TRACK=1`.

Do **not** use `--all`: it expands to `skill:['*'], agent:['*'], yes:true` and
targets all 76 agents.

### The `--bare` collision (this is the one that bites)

`claude --bare` is what the docs recommend for CI — *"the recommended mode for
scripted and SDK calls, and will become the default for `-p` in a future
release"* — and it is what you want in a container for reproducibility. But:

- Observed: `--bare` from a cwd containing **both** `~/.claude/skills/p-homeclaude`
  and `<cwd>/.claude/skills/p-claudeproj` yields `slash_commands: []`. Neither
  the personal nor the project skill loaded.
- Observed: `claude --bare -p "/p-homeclaude"` returns
  `"Unknown command: /p-homeclaude"` with `is_error: false, subtype: success`.
- Observed: `--bare --add-dir <project>` **does** load
  `<project>/.claude/skills/` (`p-claudeproj` appeared).
- The skills doc sentence *"Bare mode still loads them"* is scoped to
  **additional directories** — it sits in the `--add-dir` section, and my
  measurement agrees with it. It does **not** mean `~/.claude/skills` or the
  cwd's own `.claude/skills` load under `--bare`; those do not.
- The `--bare` help text's *"Skills still resolve via `/skill-name`"* **is**
  misleading as written: measured, a `~/.claude/skills` skill does not resolve.
  Read it as "skills that bare mode loaded (i.e. from `--add-dir` /
  `--plugin-dir`) can still be invoked by name".

**Consequence for the design:** if the container runs `skills add -g` and then
`claude --bare`, the skills are invisible and the Run silently proceeds without
them. Three ways out, cheapest first:

1. **Don't use `--bare`.** Accept that the container reads `~/.claude` and the
   cloned repo's `.claude/settings.json`. In a single-purpose ephemeral
   container this is a small blast radius, and it is zero extra machinery.
2. **Install project-scoped** — `skills add <source> -a claude-code codex -s '*' -y`
   without `-g`, which writes `<cwd>/.claude/skills/` (claude) and
   `<cwd>/.agents/skills/` (codex root r4). Both CLIs find them. Still needs
   `--bare` off for claude.
3. **`--bare` plus `--add-dir <workspace>`** with project-scoped skills. Works
   (measured), but adds a flag and only covers `.claude/skills/`, not
   `.claude/commands/` or `.claude/agents/`.

Recommend (1) for v1 and note the ceiling: if `--bare` becomes the default for
`-p` in a future Claude Code release, this breaks and we move to (3).

**Divergence:** `codex` has no `--bare` equivalent that disables skills, but it
does have `--ignore-user-config` (config only, auth still uses `CODEX_HOME`) and
`--ignore-rules`. Neither disables skill discovery. So skills "just work" for
codex and are conditional for claude.

---

## Facts checked and their versions

| Fact | Source | Version / date |
| --- | --- | --- |
| `claude --help`, hidden option table, result-subtype enum | the installed binary at `/opt/homebrew/Caskroom/claude-code@latest/2.1.251/claude` | 2.1.251 |
| `claude` exit codes, `result` shapes, `--max-turns` / `--max-budget-usd` behaviour, `--bare` skill visibility, `CLAUDE_CONFIG_DIR`, stream event types | executed locally | 2.1.251, 2026-08-31 |
| `claude -p` semantics, SIGTERM→143, stdin 10 MB cap, subagent forwarding, `system/init` fields | `https://code.claude.com/docs/en/headless` (redirected from `docs.claude.com/en/docs/claude-code/headless`) | fetched 2026-08-31 |
| `--max-turns` / `--max-budget-usd` descriptions | `https://code.claude.com/docs/en/cli-reference` | fetched 2026-08-31 |
| Claude skill locations, precedence, symlink support, bare-mode behaviour | `https://code.claude.com/docs/en/skills` | fetched 2026-08-31 |
| `ANTHROPIC_API_KEY` / `ANTHROPIC_AUTH_TOKEN` / telemetry vars | `https://code.claude.com/docs/en/env-vars` | fetched 2026-08-31 |
| `codex --help`, `codex exec --help`, `codex login --help`, `codex doctor`, `codex debug prompt-input` | the installed binary at `/opt/homebrew/Caskroom/codex/0.151.0/bin/codex` | 0.151.0 |
| codex exit codes, `--json` event shapes incl. `turn.failed`, rollout file contents, `CODEX_API_KEY` vs `OPENAI_API_KEY`, skill roots, SIGTERM→143, `developer_instructions` / `personality` / `AGENTS.md` injection | executed locally; skill roots and prompt injection via `codex debug prompt-input`, which renders the model-visible prompt with no API call | 0.151.0, 2026-08-31 |
| codex non-interactive semantics, "No explicit turn/iteration limits documented" | `https://learn.chatgpt.com/docs/non-interactive-mode` (308 from `developers.openai.com/codex/noninteractive`) | fetched 2026-08-31 |
| codex hook events, `permissionDecision: deny`, hook trust | `https://learn.chatgpt.com/docs/hooks`, cross-checked against strings in the 0.151.0 binary | fetched 2026-08-31 |
| `skills` CLI agent registry, canonical store, copy-vs-symlink, source parsing, exit codes | `github.com/vercel-labs/skills` source (`src/agents.ts`, `src/installer.ts`, `src/add.ts`, `src/source-parser.ts`, `src/skill-lock.ts`) plus real runs in isolated `HOME`s | npm `skills` **1.5.23** (repo HEAD == `latest`), 2026-08-31 |
| `skills` CLI lockfile layout | `~/.agents/.skill-lock.json` (`"version": 3`) on this machine | observed 2026-08-31 |
| Node runtime available for `npx skills` | `node v22.23.2` | 2026-08-31 |

**Note on doc URLs:** both vendors moved their docs. `docs.claude.com/en/docs/claude-code/*`
→ 301 → `code.claude.com/docs/en/*`. `developers.openai.com/codex/*` → 308 →
`learn.chatgpt.com/docs/*`. Any link we write down should use the new hosts.

---

## Open / UNKNOWN

- **`claude --task-budget <tokens>`.** Present in the 2.1.251 binary
  (*"API-side task budget in tokens (output_config.task_budget)"*), hidden, and
  absent from the docs. Whether it terminates a run, what happens when it is
  hit, and whether it is stable are all **UNKNOWN**. Settled by: running it with
  a tiny value and inspecting the `result` event; or by it appearing in the
  public CLI reference.

- **Whether `--max-turns` is being deprecated.** It is `hideHelp()`'d in 2.1.251
  while still documented. **UNKNOWN.** Settled by: an Anthropic changelog entry,
  or watching whether it stays hidden across the next few releases. If the
  Agent YAML hard-depends on it, this is a real risk.

- **Whether `codex` will ever gain a turn cap.** **UNKNOWN.** Settled by: an
  `openai/codex` issue or release note. Nothing in 0.151.0.

- **Whether a codex `Stop` hook can hard-terminate a run.** The binary contains
  `continue` / `stopReason` handling for `Stop`, and explicitly rejects
  `continue:false` for `PreToolUse`. Whether `Stop` + `continue:false` ends the
  process (as opposed to preventing the agent from stopping) is **UNKNOWN**.
  Settled by: a 20-line hook experiment, ~5 minutes.

- **Whether `codex exec` ever exits with something other than 0/1/2/143.**
  Verified empirically for those four (success, `turn.failed`, bad argv,
  SIGTERM). A complete enumeration from the Rust source was **not** obtained.
  Settled by: grepping `std::process::exit` / `ExitCode` in `codex-rs/exec/` and
  `codex-rs/cli/` at the 0.151.0 tag. Low risk — the four cover our cases.

- **The complete `codex --json` item-type list.** Observed `agent_message`,
  `command_execution`, `file_change`, `error`. The docs also list `reasoning`,
  `mcp_tool_call`, `web_search`, `plan_update`. Not exercised, so their exact
  field shapes are **UNKNOWN**. Settled by: a run that uses each, or the Rust
  `ThreadItem` enum.

- **`OPENAI_API_KEY` in codex.** The string is in the binary and the public docs
  mention it, but it demonstrably does not authenticate `codex exec` in
  0.151.0. Where it *is* read (custom `model_provider`? `--oss`? `codex login`?)
  is **UNKNOWN**. Does not block us — `CODEX_API_KEY` works — but worth a
  comment in the entrypoint so nobody "fixes" it later.

- **Journal normalisation.** Nothing here decides whether the Journal stores two
  raw formats or one translated one. That is a schema decision for a later
  ticket; this ticket only establishes that a passthrough single format is not
  available.

- **Cost accounting for codex Runs.** map.md lists "Cost and token accounting per
  Run" as not-yet-specified. Resolution: **free for `claude`**
  (`total_cost_usd` + `modelUsage` in the `result` event), **tokens only for
  `codex`** (`turn.completed.usage`); converting codex tokens to dollars needs a
  price table we would have to maintain. Recommend recording tokens for codex
  and not pretending to a dollar figure.
