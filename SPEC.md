# Autonomous Agents — v1 specification

Autonomous Agents runs LLM CLI agents on a schedule or a webhook, one Docker
container per execution, defined entirely by YAML files in git.

This document is what a fresh session implements from. It is the output of the
map in `.scratch/v1-spec/`, where every decision below is recorded with the
reasoning and the evidence that produced it. Vocabulary is defined in
`CONTEXT.md` and used precisely throughout — in particular **Agent** is a YAML
definition and **Run** is one execution of it; they are never interchangeable.

Decisions that are hard to reverse have their own ADRs in `docs/adr/`.

---

## 1. Architecture

```
        webhook ──┐
                  ├──> control plane ──> Docker API ──> Run container
        schedule ─┘     (Coolify, Go)         │              │
                          │  ▲                │              │
                     UI ──┘  └────────────────┴──────────────┘
                                 Run API (Tailscale)
                                        │
                              object storage (Journal)
```

Four components:

- **Control plane** — one Go binary. Reads Agent YAMLs, serves webhooks and a
  scheduler, spawns Runs over the Docker API, serves the Run API and a read-only
  UI. Runs in Coolify.
- **Base image** — one shared image containing both CLIs and their tooling.
- **`dsecrets`** — a shell script in that image which fetches named secrets and
  `exec`s a child with them in its environment.
- **Journal** — S3-compatible object storage holding what every Run did.

---

## 2. Agent YAML

One Agent, one file, discovered by globbing `agents_dir`.

```yaml
name: linear-triage            # required, unique
agent: claude                  # required: claude | codex
prompt: |                      # required, static — no templating
  Read /run/trigger.json. It is a Linear webhook payload.

runner: local                  # default: local
personality: |                 # default: ""
  You triage incoming issues. You are terse and you do not speculate.

skills:                        # default: []
  - vercel-labs/agent-skills --skill frontend-design
  - https://github.com/oter/my-skills.git

repos:                         # default: []
  - url: git@github.com:oter/some-service.git
    path: workspace            # cloned to /run/workspace

secrets:                       # default: {} — this map IS the Allowlist
  ANTHROPIC_API_KEY: |
    -----BEGIN AGE ENCRYPTED FILE-----
    ...

limits:
  wall_clock: 15m              # default 30m — the only universal limit
  memory: 2g                   # default conservative, never unbounded
  cpus: "1.5"
extra_args: ["--max-budget-usd", "0.50"]   # passed to the CLI verbatim

triggers:                      # default: [] — legal; UI-only Agent
  - kind: webhook
    path: /hooks/linear-triage
    auth:
      scheme: hmac_sha256      # hmac_sha256 | bearer | none
      header: Linear-Signature
      encoding: hex            # hex | base64
      secret: |
        -----BEGIN AGE ENCRYPTED FILE-----
        ...
  - kind: schedule
    cron: "0 3 * * *"
    timezone: Europe/Berlin
    catch_up: false            # default — missed ticks are skipped, not replayed

max_concurrent: 1              # default
```

**Required**: `name`, `agent`, `prompt`. Everything else defaults.

**`personality`** maps to `--append-system-prompt` for claude and
`-c developer_instructions=` for codex. These are not equivalent — claude
appends, codex replaces, and codex has no append variant. The schema records the
difference rather than pretending one key means one thing.

**`limits.wall_clock` is the only universal limit**, because neither CLI has a
wall clock of its own. There is deliberately **no unified token or budget key**:
claude caps dollars (`--max-budget-usd`, which overshot a $0.0001 cap by 350x in
testing — a stop condition, not a guarantee), and codex caps weighted tokens
through an undocumented, off-by-default, under-development config key. CLI-
specific caps go through `extra_args`, passed verbatim, so the schema does not
change when a vendor renames a flag.

**`memory` and `cpus` matter more than they look.** On the `local` Runner, Runs
share a Docker host with the control plane that supervises them. Unbounded Runs
at `max_concurrent: 6` degrade the whole platform rather than one Agent.

**`repos` declares which repositories, never which commit.** Teardown has to know
what to push, so the Agent declares it. Checking out a specific ref — a pull
request's head SHA, say — is the agent's own job, done by reading
`/run/trigger.json`. No templating exists anywhere in the schema.

**`secrets` is the Allowlist.** Values are age ciphertext encrypted to the
control plane's master identity. Use a `|` literal block, never `>` — folded
scalars mangle armored age.

**Validation**: a malformed Agent file **fails the entire startup**. Configuration
comes from reviewed git, and a loudly failed deploy beats one Agent silently not
running.

**No hot reload.** A change needs a redeploy, which in Coolify *is* the reload.

---

## 3. Control-plane config

```yaml
listen:
  hooks: ":8080"     # PUBLIC — per-Agent HMAC/bearer is the only guard
  ui:    ":8081"     # PRIVATE (tailnet) + basic auth
  run:   ":8082"     # PRIVATE (tailnet) + per-Run bearer token

ui:
  username: oter
  password_bcrypt: "$2a$12$..."

agents_dir: ./agents
image: ghcr.io/oter/autonomous-agents/agent:2026-08-31
stop_grace: 90s
control_plane_url: http://100.64.0.1:8082   # listen.run as reached from a Run

runners:
  local:
    docker_host: "unix:///var/run/docker.sock"
    max_concurrent: 6
  macmini:
    docker_host: "unix:///shared/macmini-docker.sock"
    max_concurrent: 2

secrets:
  master_identity: /etc/autonomous-agents/age-master.key   # 0600

journal:
  endpoint: https://<account>.r2.cloudflarestorage.com
  bucket: agentruns
  region: auto       # default; what R2 wants, and what the sandbox accepts
  # The credential is AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY in the
  # control plane's environment, never in this file; containers get
  # presigned URLs.
```

**The route surface is split three ways because it has to be.** Linear and
GitHub live on the internet and cannot POST to a private network, so `/hooks/*`
is public and guarded solely by the per-Agent HMAC or bearer secret. The UI and
the Run API are reachable only over the tailnet.

**Both Runners are plain unix sockets, so the control plane branches on
nothing.** The remote one is a socket forwarded by an `autossh` sidecar:

```
autossh -M 0 -N \
  -o ServerAliveInterval=15 -o ServerAliveCountMax=3 \
  -o ExitOnForwardFailure=yes -o StrictHostKeyChecking=accept-new \
  -L /shared/macmini-docker.sock:<remote-socket> runner@macmini
```

This is deliberately **not** the Docker SDK's `ssh://` support, for a reason
worth stating: the SDK does not have any. `client.WithHost("ssh://user@host")`
returns no error and then TCP-dials the literal string `user@host`; `ssh://`
lives in `docker/cli`'s connhelper package. Using it would mean a heavyweight
module dependency, a `docker` CLI on the remote host's non-interactive `PATH`, a
hidden `dial-stdio` command documented as "should not be invoked manually", flaky
API-version negotiation, a fresh SSH handshake per connection, and — worst — no
liveness detection whatsoever, since `SetDeadline` is a no-op on that transport
and a silently stalled link hangs forever. The forwarded socket deletes all of
it for one supervised process.

---

## 4. Base image

One shared image for every Agent. No per-Agent images, no build pipeline, no
registry beyond this one image. It is published as
`ghcr.io/oter/autonomous-agents/agent`; that last word names what a Run's
container executes, not an Agent in the glossary's sense.

Contains: `claude`, `codex`, the `skills` CLI (`npm i -g skills`, so `npx` does
not re-download it every Run), `curl`, `jq`, `git`, `tar`, `zstd`, `iptables`,
`dsecrets`, and the entrypoint. For the work itself: Node 24 with `yarn` via
corepack, the Go toolchain, and `build-essential`. It does **not** contain
`age` — decryption is the control plane's job.

Skills are installed **project-scoped** at container start, so both CLIs find
them. **Never pass `claude --bare`**: the documentation recommends it for CI, but
measured, it yields `slash_commands: []` — the Run proceeds with no skills, no
error, and no way to tell.

---

## 5. The Run lifecycle

1. A Trigger fires: a webhook whose signature verifies, a scheduler tick, or the
   UI's "run now".
2. The control plane checks `max_concurrent` for the Agent and for the Runner.
   Over the limit, the Run is **queued**, not dropped.
3. It allocates a run id — `20260831-201204-<agent>-<4 hex>` — and mints an
   opaque `RUN_TOKEN`.
4. It creates the container on the Agent's Runner with `--cap-add NET_ADMIN`,
   the configured memory and CPU limits, and the per-Run environment.
5. The entrypoint runs (§6).
6. The control plane polls `ContainerInspect` and receives heartbeats.
7. Teardown pushes the work branch and uploads the Journal.
8. The control plane records the outcome, preferring `Inspect`'s exit code.

---

## 6. Entrypoint and Teardown

```sh
#!/bin/sh
set -eu
JOURNAL=/run/journal; mkdir -p "$JOURNAL" /run/workspace

touch "$JOURNAL/stream.jsonl" "$JOURNAL/stderr.log"
CLI_VERSION=$("$AGENT_CLI" --version 2>/dev/null) || CLI_VERSION=

api()    { curl -fsS --retry 3 --max-time 10 -H "Authorization: Bearer $RUN_TOKEN" "$@"; }
report() { api --data "$(jq -cn --arg s "$1" --arg m "${2:-}" --argjson c "${3:-null}" \
             '{status:$s,message:$m,exit_code:$c}')" "$CONTROL_PLANE_URL/run/status" || true; }
fail()   { echo "entrypoint: $*" >&2; report failed "$*"; exit 1; }

teardown() {
  rc=$?; trap - EXIT; set +e
  kill "$HEARTBEAT" "$LIMIT" 2>/dev/null
  kill -TERM "$AGENT" 2>/dev/null

  find "$CODEX_HOME" "$CLAUDE_CONFIG_DIR" \
       \( -name '*.jsonl' -o -name '*.jsonl.zst' \) \
       -exec cp {} "$JOURNAL/" \; 2>/dev/null

  push_work && w=pushed || w=failed
  urls=$(api "$CONTROL_PLANE_URL/run/journal-urls") || urls='{}'
  write_meta "$rc" "$w" "$urls" > "$JOURNAL/meta.json" || rm -f "$JOURNAL/meta.json"   # §10
  tar --zstd -cf /run/run.tar.zst -C "$JOURNAL" .
  journal="journal upload skipped"
  [ "$urls" = '{}' ] || {
    journal="journal uploaded"
    curl -fsS -T "$JOURNAL/meta.json" "$(echo "$urls" | jq -r .meta)" || journal="journal upload failed: meta"
    curl -fsS -T /run/run.tar.zst     "$(echo "$urls" | jq -r .archive)" || journal="journal upload failed: archive"
  }
  report finished "$journal" "$rc"
  exit "$rc"
}
trap teardown EXIT
trap 'exit 143' TERM INT

install_egress_rules                                    # §7
api "$CONTROL_PLANE_URL/run/payload" -o /run/trigger.json || fail "payload fetch"
( while sleep 30; do report running; done ) & HEARTBEAT=$!

[ -z "${AGENT_SKILLS:-}" ] || timeout "${SKILLS_TIMEOUT:-300}" \
  install_skills "$AGENT_SKILLS" || fail "skills install"
clone_repos "${AGENT_REPOS:-}" || fail "clone"

( sleep "$WALL_CLOCK_SECONDS"; kill -TERM 1 )  & LIMIT=$!

build_argv                                              # per AGENT_CLI
setpriv --reuid agent --regid agent --clear-groups "$@" \
  >"$JOURNAL/stream.jsonl" 2>"$JOURNAL/stderr.log" </dev/null &
AGENT=$!
wait "$AGENT"
```

Every awkward line above is load-bearing:

- **`& wait $!`, never foreground.** A shell trap does not fire while a
  foreground command is running. Without this, the grace period buys nothing and
  Teardown never runs at all.
- **`trap 'exit 143' TERM` on top of `trap teardown EXIT`.** The TERM handler's
  only job is to cause an exit, which routes a stop-kill through the single
  teardown path. One implementation, every exit path.
- **stdout to a file, never a pipe.** `claude` waits up to 30 seconds draining
  stdout if its consumer reads slowly, which would consume the whole grace before
  the Journal upload.
- **`</dev/null`.** `codex` hangs if stdin is left open.
- **The internal `sleep && kill -TERM 1`** is mandatory, not defensive: a codex
  network partition retries **forever** with no timeout, and the control plane
  cannot kill what it cannot reach.
- **Both globs in the `find`.** codex compresses rollouts when cold, so a
  `*.jsonl` glob silently drops them.
- **The heartbeat starts before skills install and clone.** Each has its own
  timeout of minutes, and three missed heartbeats mark a Run stale (§9); a
  Run quietly installing for two minutes must not be one. The wall clock
  still starts after them, because the install does not consume it.
- **`setpriv` drops to an unprivileged user**, and with it `NET_ADMIN`, so the
  agent cannot flush the egress rules. Teardown remains in the root shell, which
  is what lets it push even if the agent has wedged its own session.
- **`set +e` first thing in Teardown.** Under `set -e` a failed `curl` would
  end the function, and the container would exit with curl's status instead
  of the Run's. Every step below it reports its failure and carries on to
  `exit "$rc"`; the upload's outcome travels in the finished report's
  message, and only the exact message `journal uploaded` lets the control
  plane remove the exited container. Any other keeps it, as the only copy.
- **`journal-urls` before `write_meta`.** The URLs are minted at Teardown so a
  ninety-minute Run cannot outlive them ([ADR-0005](docs/adr/0005-journals-in-object-storage.md)),
  and the same reply carries the Run's throttle count, which `meta.json`
  records (§10). A Run that cannot reach the control plane skips the upload
  and still writes its Journal into the container.
- **`--retry 3` in `api`.** A Run that drained its token bucket in its last
  seconds gets 429 at Teardown, and without a retry that would cost it its
  Journal; curl waits the `Retry-After` the API sends (§9). A refused
  connection is not retried, so a Run whose control plane is gone does not
  stall on every heartbeat.
- **`touch` the stream and stderr files first.** A Run that aborts before its
  agent starts still has the two files §10 lists, and an empty stream
  summarises to `no_terminal_event` rather than to nothing. A `write_meta`
  that fails removes its output: an empty `meta.json` must never be uploaded.
- **The control plane removes an exited container only after it has seen
  both objects in the bucket itself**, with a `HEAD` on presigned URLs of its
  own. The finished report's message says what the container thinks
  happened; it is logged, never trusted for the removal, because the agent
  holds the same token and could report anything. A container the bucket
  does not vouch for is kept as the only copy of its Journal.

**Failure policy.** Payload fetch, skills install, and clone each **abort** on
failure. An Agent that declares skills has a prompt that assumes them, so
proceeding without is a silent behaviour change. Every abort still runs Teardown,
because the trap is installed first — a failed Run leaves a Journal explaining
itself. Skill installation has its own timeout and does not consume the Run's
wall clock.

**Push order is work first, Journal last**, so `meta.json` records whether the
work push succeeded.

---

## 7. Network egress

A Run reaches the internet and the control plane. Nothing else.

```
allow → the internet                       (git, npm, model APIs)
allow → control plane tailnet IP, one port
drop  → 100.64.0.0/10                      (the rest of the tailnet)
drop  → 10/8, 172.16/12, 192.168/16
```

Enforced with `iptables` **inside** the container rather than in the host's
`DOCKER-USER` chain. The host route breaks on macOS, where Docker's chains live
inside a Linux VM; in-container rules behave identically on both Runners. This is
why the agent must run unprivileged.

---

## 8. `dsecrets`

```sh
#!/bin/sh
# dsecrets NAME[,NAME...] -- command [args...]
set -eu

names=${1:?usage: dsecrets NAME[,NAME...] -- cmd}
shift
[ "${1:-}" = "--" ] || { echo "dsecrets: expected -- before the command" >&2; exit 2; }
shift

resp=$(curl -fsS --max-time 10 \
  -H "Authorization: Bearer $RUN_TOKEN" -H 'Content-Type: application/json' \
  --data "$(jq -cn --arg n "$names" '{names: ($n | split(","))}')" \
  "$CONTROL_PLANE_URL/run/secrets")

for n in $(echo "$names" | tr ',' ' '); do
  printf '%s' "$resp" | jq -e --arg n "$n" 'has($n)' >/dev/null || {
    echo "dsecrets: control plane did not return: $n" >&2; exit 3; }
  v=$(printf '%s' "$resp" | jq -j --arg n "$n" '.[$n]'; printf X)
  export "$n=${v%X}"
done

exec "$@"
```

- **`exec`, not fork.** Signals reach the child because nothing is left in the
  way. `sops exec-env` was measured orphaning its child on `SIGTERM`, which in
  this system would silently lose the Journal of every timed-out Run.
- **`has($n)` before the value is read.** The obvious `v=$(jq -er …) || fail` is
  broken: a command substitution's exit status is its *last* command.
- **`jq -j` plus the `X` sentinel** keeps values byte-exact — `-j` drops jq's
  added newline, the sentinel survives `$()` stripping a genuine one.
- **No file sink and no decrypt-everything mode.** Naming what a command needs is
  the entire discipline.
- The secret name **is** the environment variable name.

**The accepted hole**: the model credential (`ANTHROPIC_API_KEY` /
`CODEX_API_KEY`) is plaintext in the agent's own environment, because the CLI
consumes it. So the guarantee is the narrower one — an agent can always read the
credential it is currently spending, and everything else stays behind
`dsecrets`.

---

## 9. The Run API

Plain HTTP over the Tailscale tailnet. **No TLS** — WireGuard already provides
encryption and device identity, and a certificate lifecycle on top would buy
nothing.

```
GET  /run/payload      → 200 raw trigger body; {} for a schedule Trigger
POST /run/status       → {status, message, exit_code} → 204
POST /run/secrets      → {names:[...]} → 200 {NAME: value} | 403 {denied:[...]}
GET  /run/journal-urls → 200 {meta: <presigned PUT>, archive: <presigned PUT>, throttle_events: n}

401 absent/bad token · 403 denied or terminal Run · 404 unknown Run · 429 throttled
```

- **A denied name is a 403 naming the names, never a silent omission.**
- **The token is opaque and stored, not a JWT** — the control plane already holds
  a Run record, so revocation is instant and there is no denylist to maintain.
  The token is the only identity the API sees, so a *bad* token is one that
  cannot be a `RUN_TOKEN` at all (absent, not `Bearer`, not 32 base64url
  bytes), and an *unknown Run* is a well-formed token no Run holds — after a
  control-plane restart, that is an orphan container, and it is logged as one.
- **Abuse is throttled and surfaced, never auto-killed.** A per-Run token bucket
  (sized in code; every route counted) returns 429 with `Retry-After`; each
  refusal is a throttle event on the Run, and the Run appears flagged in the
  UI and its Journal. A threshold that kills is a guess,
  and it fails by killing a legitimate ninety-minute Run at minute eighty-five.
- **Three missed heartbeats mark a Run stale and trigger an immediate
  `ContainerInspect`.** A gap is a hint to ask Docker, never a conclusion.
- **First terminal state wins**, and **`ContainerInspect` is authoritative** on
  disagreement.
- **`journal-urls` carries the throttle count** because it is the one end-of-Run
  fact `meta.json` records (§10) that only the control plane holds. It is a
  fact about the Run itself, read-only, so it stays inside the principle
  below; it is not a second status channel.
- **If the control plane is unreachable, the Run continues.** It holds its
  payload and its repos, and Teardown pushes the work branch without help.
  Commands needing a secret fail until the link returns.

**Governing principle: the Run API is read-only about the Run itself and
write-only about its own status.** A Run cannot start another Run, read another
Run's data, list Agents, or touch configuration. This is the one channel a
language model with a shell can reach the control plane through, so the default
answer to "could we also expose…" is no.

---

## 10. The Journal

```
<agent>/<run-id>/meta.json       small, flat, stable schema
<agent>/<run-id>/run.tar.zst     event stream, stderr, CLI rollout files
```

`ListObjects` is the index; there is no index file, and therefore nothing two
Runs both write.

**`meta.json` at start** carries what lets a behaviour change be correlated with
a configuration change — the point of keeping Journals at all: Agent name **and
the SHA-256 of its YAML**, Runner, trigger kind and name, CLI name **and
version**, base image digest, limits in effect, resolved skill versions, and the
prompt and personality verbatim.

**At end**: exit code; `terminal_reason` read from **the event stream, not
`$?`**, because sandbox denials exit 0 and `error_seen` is sticky; duration; the
work branch and whether its push succeeded; throttle events; and cost —
`total_cost_usd` for claude, **tokens only** for codex, a divergence the schema
records rather than papers over.

**The schema**, flat and stable, written by the entrypoint's `write_meta` from
three sources: `RUN_META`, the JSON the control plane puts in the environment
at spawn with what only it knows (`agent_sha256`, `runner`, `trigger_*`,
`image`, `image_digest`, the limits); the entrypoint's own facts; and a
summary of `stream.jsonl` computed by `stream.jq` in the image.

```json
{
  "run_id": "20260902-142724-hello-1dd6", "agent": "hello", "agent_sha256": "<hex>",
  "runner": "local", "trigger_kind": "manual", "trigger_name": "run-now",
  "cli": "claude", "cli_version": "2.1.258 (Claude Code)",
  "image": "ghcr.io/oter/autonomous-agents/agent:2026-09-04", "image_id": "sha256:<hex>",
  "image_digest": "ghcr.io/oter/autonomous-agents/agent@sha256:<hex>",
  "wall_clock_seconds": 300, "memory": "1g", "cpus": "1",
  "prompt": "<verbatim>", "personality": "<verbatim>",
  "started_at": "2026-09-02T14:27:24Z", "ended_at": "2026-09-02T14:27:25Z", "duration_seconds": 1,
  "exit_code": 0, "terminal_reason": "completed", "is_error": false, "error": null,
  "num_turns": 1, "total_cost_usd": 0.17, "usage": {"<the CLI's own usage object>": 0},
  "permission_denials": 0, "error_events": null, "failed_items": null, "events": 4,
  "work_branch": null, "work_push": "none", "throttle_events": 0
}
```

- `terminal_reason` is claude's own value from its `result` event
  (`completed`, `api_error`, `max_turns`, `budget_exhausted`, …), and for
  codex `completed` or `failed` from whichever of `turn.completed` and
  `turn.failed` came last. Either CLI gets `no_terminal_event` when the
  stream ended without one: killed at the wall clock, stopped, or crashed.
  A claude `result` event whose `terminal_reason` is itself `null` (measured
  once, for an unknown slash command) stays `null`; `num_turns` shows the
  event existed. `unparsed` means the summary itself failed, which is a
  broken image, not a kind of Run. `error` is claude's `result` text when
  `is_error`, and codex's `turn.failed` message.
- **A field the CLI does not report is `null`, never zero.** codex reports no
  dollars, no `is_error`, no `permission_denials`; claude has no top-level
  `error` events and no `failed_items`. `usage` is each CLI's own object,
  field names untouched, summed across turns for codex.
- `image_id` is Docker's content id of the image, present for any image;
  `image_digest` is the registry digest it was pulled by, absent for a local
  build. `cpus` is absent when unlimited. `throttle_events` is `null` when the
  control plane could not be reached at Teardown, in which case nothing was
  uploaded either. `work_branch` is `null` and `work_push` is `none` until
  ticket 11 pushes; resolved skill versions arrive with the same ticket.

**Parser notes**, all measured: `web_search` serialises `id` twice; `declined`
patches fold into `failed`; `error` events carry no `will_retry`, so retry noise
is shape-identical to a fatal error; codex rollout directories are local-time
while the timestamps inside are UTC; `codex exec` writes `history_mode:
paginated` whose `TurnItem` tags are PascalCase while everything else is
snake_case. `stream.jq` honours the first three: each line is parsed on
its own so a torn last line costs nothing, a duplicated key parses last-wins,
only a terminal event decides the outcome and `error` events are counted
rather than interpreted, and `failed_items` is named for what codex actually
says. The last two are why Teardown copies the whole state tree rather than
today's date directory, and archives rollouts rather than parsing them.

**Nothing scrubs secrets.** An agent that runs `dsecrets FOO -- sh -c 'echo $FOO'`
puts the value in the stream, and nothing in the container knows the values to
redact. Private bucket; ceiling accepted.

---

## 11. What v1 is not required to do

**Ruled out of scope entirely.** Kubernetes, Nomad, Swarm, and multi-tenancy.
Per-Agent images, a build pipeline, or a registry. Browser-based YAML editing —
configuration lives in git. Automatic pull requests or merging of agent work; a
Run pushes a branch and stops. **Chained Runs** — the Run API forbids a Run
starting another Run, and that is a security boundary, not an unimplemented
feature.

**Deferred, and expected later.** Live log streaming to the UI, whose obvious
home is the Run API. Agent-authored decision notes (`agentrun note`) on top of
the mechanical stream. Retry, backoff, dead-letter and alerting for failed Runs —
v1 records the failure and stops. Journal retention, now an object lifecycle rule
awaiting a policy this system has not yet produced the data to choose. Secret
rotation. Prompt templating, deferred at charting and unbroken since, including
by its strongest counterexample.

---

## 12. Open risks

**Unverified: the `macmini` Runner.** Verification is **deliberately deferred
until after the solution is built** — a scheduling decision, not an oversight. The load-bearing
test is that `docker stop --time=30` through the forwarded socket delivers
`SIGTERM`, lets the trap complete, and exits 0 rather than 137. If that fails,
Teardown never fires on a timeout kill there. Untested alongside it: whether
`NET_ADMIN` and in-container `iptables` behave the same inside Docker Desktop's
or Colima's VM. **Neither affects the `local` Runner**, which is a plain unix
socket on Linux, so implementation can proceed against `local` and any finding
produces macmini-local rework rather than a redesign.

**Docker Desktop truncates its shutdown grace to about two seconds.** A Run in
flight when the engine restarts — reboot, auto-update, sleep — can lose its
Journal. Pin the Runner to ≥ 4.88.0, or run Colima.

**`codex`'s `features.rollout_budget` is unstable.** It is
`Stage::UnderDevelopment`, undocumented, off by default, and it is codex's only
hard stop. If it disappears, `limits.wall_clock` is the only thing standing
between a wedged codex Run and its wall clock.

**`claude --max-turns` is hidden from `--help`** in 2.1.251, a soft signal it may
be de-emphasised.

**Runs and the control plane share a host on `local`.** `limits.memory` and
`limits.cpus` must be set conservatively, or Runs degrade the platform that
supervises them.

**A Run can edit its own Journal** ([ADR-0004](docs/adr/0004-the-container-writes-its-own-journal.md))
and its bearer token is equivalent to its Allowlist
([ADR-0003](docs/adr/0003-secrets-over-the-run-api.md)). Both accepted; both the
first things to revisit if the trust model changes.
