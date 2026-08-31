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
image: ghcr.io/oter/agent-base:2026-08-31
stop_grace: 90s

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
  # credentials for the control plane only; containers get presigned URLs
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
registry beyond this one image.

Contains: `claude`, `codex`, the `skills` CLI (`npm i -g skills`, so `npx` does
not re-download it every Run), `curl`, `jq`, `git`, `tar`, `zstd`, `iptables`,
`dsecrets`, and the entrypoint. It does **not** contain `age` — decryption is the
control plane's job.

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

api()    { curl -fsS --max-time 10 -H "Authorization: Bearer $RUN_TOKEN" "$@"; }
report() { api --data "$(jq -cn --arg s "$1" --arg m "${2:-}" \
             '{status:$s,message:$m}')" "$CONTROL_PLANE_URL/run/status" || true; }
fail()   { echo "entrypoint: $*" >&2; report failed "$*"; exit 1; }

teardown() {
  rc=$?; trap - EXIT
  kill "$HEARTBEAT" "$LIMIT" 2>/dev/null || true
  kill -TERM "$AGENT" 2>/dev/null || true

  find "$CODEX_HOME" "$CLAUDE_CONFIG_DIR" \
       \( -name '*.jsonl' -o -name '*.jsonl.zst' \) \
       -exec cp {} "$JOURNAL/" \; 2>/dev/null || true

  push_work && w=pushed || w=failed
  write_meta "$rc" "$w" > "$JOURNAL/meta.json"
  tar --zstd -cf /run/run.tar.zst -C "$JOURNAL" .
  urls=$(api "$CONTROL_PLANE_URL/run/journal-urls") || urls=
  [ -z "$urls" ] || {
    curl -fsS -T "$JOURNAL/meta.json" "$(echo "$urls" | jq -r .meta)"
    curl -fsS -T /run/run.tar.zst     "$(echo "$urls" | jq -r .archive)"
  }
  report finished "$rc"
  exit "$rc"
}
trap teardown EXIT
trap 'exit 143' TERM INT

install_egress_rules                                    # §7
api "$CONTROL_PLANE_URL/run/payload" -o /run/trigger.json || fail "payload fetch"
[ -z "${AGENT_SKILLS:-}" ] || timeout "${SKILLS_TIMEOUT:-300}" \
  install_skills "$AGENT_SKILLS" || fail "skills install"
clone_repos "${AGENT_REPOS:-}" || fail "clone"

( while sleep 30; do report running; done ) & HEARTBEAT=$!
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
- **`setpriv` drops to an unprivileged user**, and with it `NET_ADMIN`, so the
  agent cannot flush the egress rules. Teardown remains in the root shell, which
  is what lets it push even if the agent has wedged its own session.

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
GET  /run/journal-urls → 200 {meta: <presigned PUT>, archive: <presigned PUT>}

401 absent/bad token · 403 denied or terminal Run · 404 unknown Run · 429 throttled
```

- **A denied name is a 403 naming the names, never a silent omission.**
- **The token is opaque and stored, not a JWT** — the control plane already holds
  a Run record, so revocation is instant and there is no denylist to maintain.
- **Abuse is throttled and surfaced, never auto-killed.** A per-Run token bucket
  returns 429; the Run appears flagged in the UI and its Journal. A threshold
  that kills is a guess, and it fails by killing a legitimate ninety-minute Run
  at minute eighty-five.
- **Three missed heartbeats mark a Run stale and trigger an immediate
  `ContainerInspect`.** A gap is a hint to ask Docker, never a conclusion.
- **First terminal state wins**, and **`ContainerInspect` is authoritative** on
  disagreement.
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

**Parser notes**, all measured: `web_search` serialises `id` twice; `declined`
patches fold into `failed`; `error` events carry no `will_retry`, so retry noise
is shape-identical to a fatal error; codex rollout directories are local-time
while the timestamps inside are UTC; `codex exec` writes `history_mode:
paginated` whose `TurnItem` tags are PascalCase while everything else is
snake_case.

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

**Unverified: the `macmini` Runner.** Ticket 08 has not run. The load-bearing
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
