# 03: Walking-skeleton Run — "run now" to recorded exit

**What to build:** The tracer bullet. Hitting a run-now endpoint for an Agent starts a real Run: a container spawns on the `local` Runner, the CLI executes the Agent's prompt, and the control plane records the outcome. From this ticket on, the system demonstrably runs agents end to end; everything later widens this path.

**Blocked by:** 01 (Agent YAML + config loader), 02 (Base image).

**Status:** resolved

- [x] Run id allocated in the documented format (timestamp, agent name, hex suffix) and an opaque `RUN_TOKEN` minted per Run — stored, not a JWT
- [x] Container created over the local unix socket with the Agent's memory/cpu limits and per-Run environment; stop grace from config
- [x] Minimal Run API for the entrypoint: payload endpoint returning `{}` for now, status endpoint accepting reports; 401 on absent/bad token
- [x] Entrypoint follows the SPEC §6 shape exactly — every awkward line there is load-bearing and measured: agent started with `& wait` (never foreground, or the EXIT trap can't fire), `trap 'exit 143' TERM` routing stop-kills through the one teardown path, stdout to a file (claude stalls on slow pipes), `</dev/null` (codex hangs on open stdin), and the internal wall-clock `sleep && kill -TERM 1` (a codex network partition retries forever; the control plane cannot kill what it cannot reach)
- [x] Teardown runs on every exit path — success, failure, TERM at the wall clock — and leaves a local Journal directory (stream, stderr, CLI rollout files including the compressed `.zst` variants, a minimal meta)
- [x] Control plane polls ContainerInspect; outcome recorded preferring Inspect's exit code; first terminal state wins
- [x] Demo: run-now on a trivial Agent completes; the same Agent with a tiny `wall_clock` is TERM-killed and still leaves its Journal

## Answer

Implemented as three small packages plus the entrypoint:

- `internal/docker` — the sliver of the Engine API the control plane needs
  (create, start, inspect) over a unix socket with `net/http`. No Docker SDK:
  both Runners are plain unix sockets (SPEC §3), and the SDK is a heavyweight
  module for four HTTP calls. 30s client timeout so a stalled socket fails
  instead of hanging the poller.
- `internal/run` — `NewID`/`NewToken` (SPEC §5/§9 formats), the in-memory
  `Store` (first terminal state wins, `Finish` from Inspect overrides a
  container's own exit code), the Run API handler (`GET /run/payload` → `{}`,
  `POST /run/status` → 204, 401 on an absent, schemeless, or unknown bearer),
  and the `Spawner` (container named by run id, `StopTimeout` from
  `stop_grace`, `Memory` with `MemorySwap` equal so no swap, `NanoCpus`, the
  per-Run env, then a 5s `ContainerInspect` poll).
- `cmd/control-plane` — loads config, one Docker client per Runner, serves
  the Run API on `listen.run` and `POST /agents/{name}/run` on `listen.ui`
  behind basic auth (`ui.password_bcrypt`). Responds 202 `{"id": ...}`.
- `image/entrypoint.sh` — SPEC §6 line for line for what exists in this
  ticket: `trap teardown EXIT` then `trap 'exit 143' TERM INT`, the internal
  `sleep && kill -TERM 1`, `build_argv` as a `case` on `AGENT_CLI` (never
  `--bare`; `--dangerously-skip-permissions` / `--dangerously-bypass-approvals-and-sandbox`
  because the container is the sandbox), `setpriv` to `agent`, stdout and
  stderr to files, `</dev/null`, `& wait`. Teardown collects `*.jsonl` and
  `*.jsonl.zst` from `CODEX_HOME`/`CLAUDE_CONFIG_DIR`, writes a minimal
  `meta.json`, reports `finished` with the exit code. Comments mark where
  tickets 05, 10 and 11 insert their lines. Baked into the image via
  `COPY`/`ENTRYPOINT` in `image/Dockerfile`.

Config changes (SPEC §3 updated): a required `control_plane_url` — the Run
API as reached from inside a container — because nothing else told a Run
where to call. `image` and `stop_grace` are now also required at startup
(a zero grace would SIGKILL right after TERM and lose every Journal), and
`limits.memory`/`cpus`/`wall_clock` are parsed and rejected at startup rather
than on the first Run.

### Demo (2026-09-01, OrbStack, image built locally as `agent-base:dev`)

`AA_IMAGE=agent-base:dev go test ./internal/run -run WalkingSkeleton -v`
is the demo, kept as a guarded test. Both paths pass:

- trivial claude Run: container spawned, entrypoint fetched `{}` payload,
  CLI ran (`system/init` event with `permissionMode: bypassPermissions`,
  slash commands populated — skills would be discovered), exited 1 because no
  `ANTHROPIC_API_KEY` was in the environment (`result` event
  `is_error: true`), Teardown reported `finished`, Inspect confirmed exit 1.
  Journal: `stream.jsonl`, `stderr.log`, the transcript `.jsonl`, `meta.json`.
- codex Run with `wall_clock: 5s`: killed by the internal wall clock while
  codex was retrying its 401s (the SPEC's exact motivating scenario), exit 143
  from the trap, Journal with `stream.jsonl`, `stderr.log`, the rollout
  `.jsonl`, `meta.json` `{"exit_code": 143}`. The manual run through the
  binary (`curl -u oter:… -X POST :8081/agents/sleepy/run`) recorded the same.

The exit-0 path was then verified the same evening with a credential: claude
Runs use the Claude subscription only (`claude setup-token` mints a
long-lived token, exported as `CLAUDE_CODE_OAUTH_TOKEN` in the control
plane's environment). With it set, the trivial Run answered the prompt and
exited 0 in under four seconds, Teardown reported `finished`, Inspect
confirmed exit 0, and the wall-clock Run still exited 143. All three exit
paths of the EXIT trap (0, 1, 143) have now run for real. `ANTHROPIC_API_KEY`
is never forwarded (the CLI would prefer it and bill API credits), and the
control plane itself makes no Anthropic API calls.

### Deliberate shortcuts, all marked `ponytail:` in code

- Run records are in memory; forgotten on restart.
- The model credential (`CLAUDE_CODE_OAUTH_TOKEN` for claude,
  `CODEX_API_KEY` for codex) is copied from the control plane's own
  environment into Runs of that CLI kind. Ticket 06 must delete this, not
  keep it as a fallback.
- Exited containers are kept so the Journal can be read with `docker cp`;
  ticket 05 removes them after upload. They accumulate until then.
- `extra_args` are passed one per line; an arg with a newline, or an empty
  arg, is unsupported.

### Follow-ups for later tickets

- `report` sends `{status, message, exit_code}` (SPEC §9's shape), so it is
  called `report finished "" "$rc"` rather than §6's `report finished "$rc"`.
- The base image needs a rebuild to carry the entrypoint: push a new date tag
  (`git tag 2026-09-02 && git push origin 2026-09-02`) or dispatch the
  workflow, then point `image:` at it. The `2026-09-01` image lacks it.
- No image pull: the Runner must already have the image, or create fails
  with `docker: not found: No such image`.
