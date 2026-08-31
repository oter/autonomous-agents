# Map: Autonomous Agents v1 spec

Label: wayfinder:map
Started: 2026-08-31

## Destination

A v1 spec in this repo that a fresh agent session can implement end to end: the
Agent YAML schema, the control plane's Run lifecycle, the container's entrypoint
and teardown contract, the Broker/`dsecrets` interface, and the Journal format —
plus ADRs for the hard-to-reverse calls.

No product code is written under this map. The map is done when nothing is left
to decide before someone goes and builds it.

## Notes

**Domain.** A Go control plane reads Agent YAML files, serves webhooks and a
schedule, and starts Runs as Docker containers on one of two Runners. Runs are
`claude` or `codex` CLIs. Vocabulary is in `CONTEXT.md` — use it, especially the
Agent/Run distinction.

**Skills every session should consult.** `grilling` and `domain-modeling` by
default. `research` for anything outside this working directory. `prototype`
when the question is "what should this look like".

**Standing preferences.** Prefer what already exists: the Docker API over custom
orchestration, `filippo.io/age` over a key-management service, the CLIs' own
flags over custom accounting, `skills` over a bespoke installer. Every ticket
should ask "does this need to exist" before it asks "how should this work".

## Decisions so far

Charting settled the following. These have no tickets — they were resolved in
conversation before the map existed — but they bind every ticket below.

- **Destination is a spec, not the build** (charting): planning only; execution
  is a separate effort.
- **Issue tracker is local markdown** (charting): the map and its tickets live
  in `.scratch/` and are committed to this public repo.
- **Control plane runs in Coolify** (charting): Docker Engine, single binary,
  not Kubernetes.
- **Two Runners** (charting): `local` (the control plane's own Docker host) and
  `macmini` (remote, reached over `ssh://`). Named once in control-plane config;
  Agents reference a Runner by name and default to `local`.
- **Secrets over the Run API** (charting, **revised twice**): see
  [ADR-0003](../../docs/adr/0003-secrets-over-the-run-api.md), which supersedes
  ADR-0002, which superseded ADR-0001. `dsecrets` asks the control plane over the
  Run API; the control plane checks the Allowlist **server-side**, decrypts with
  the master identity, logs the access, and returns plaintext. **No key material
  of any kind enters a container** — ADR-0001's original goal, reached without its
  per-Runner daemon, because ticket 06 built the channel for other reasons and
  secrets ride it for the cost of one endpoint. Per-access audit logging returns.
  The trade: a secret now needs the control plane reachable.
- **age for encryption** (charting): `filippo.io/age`, multi-recipient, no cloud
  KMS. Ciphertext inline in the Agent YAML.
- **One shared base image** (charting): both CLIs and the `skills` CLI baked in.
  Per-Agent skills installed at container start with `skills add ... -y`, which
  supports GitHub shorthand, arbitrary git URLs, and local paths, and targets
  both `claude-code` and `codex`. Skill installation gets its own timeout and
  does not consume the Run's budget.
- **Runs are ephemeral** (charting): no persistent volume in v1. Durability
  comes from the Journal.
- **The container writes at teardown** (charting): an entrypoint wrapper traps
  exit — including `SIGTERM` from a timeout kill — and pushes the Run's work to
  git and uploads the Journal to object storage. `docker stop --time=30` gives it room. The
  agent is never relied on to remember.
- **Journal shape** (charting, **backend replaced by ticket 07**): laid out
  `<agent>/<run-id>/`, run id `20260831-201204-<agent>-<4 hex>` so keys sort
  chronologically and concurrent Runs cannot collide. The **git repository is
  dropped** — Journals go to S3-compatible object storage.
- **Work pushes to a branch** (charting): `agents/<agent>/<run-id>`, always,
  never to `main`, no automatic pull request. Failed Runs push too.
- **One git credential** (charting): a fine-grained PAT with a repository
  allowlist, shared by the Journal push and the work push. Not deploy keys —
  those are inherently one per repository, which contradicts "one key".
- **Limits use what already exists** (charting, ~~turns~~ **corrected by ticket
  01**): time is a container kill via a context deadline. The turn half is dead —
  neither CLI has a usable turn abstraction, so the limit is a **token budget**.
  Still no custom accounting.
- **Split route surface** (charting): `/hooks/*` is public and guarded by
  per-Agent HMAC or bearer auth; the UI and API are private-network only, plus
  basic auth. External SaaS cannot reach a private network, so the webhook
  surface has to be exposed for Linear and similar to work at all.
- **Three webhook auth schemes** (charting): `hmac_sha256` with configurable
  header and encoding, `bearer`, and `none`. No plugin system. One HMAC scheme
  covers both GitHub and Linear.
- **The UI is read-only plus "run now"** (charting): no browser-based config
  editing — that fights the YAML-in-git premise. Schedules are declared in the
  Agent YAML.
- **Trigger payload arrives as a file** (charting): a static prompt from the
  Agent YAML, plus the raw request body written into the container for the agent
  to read and parse. No templating in v1 — adding a Trigger source should cost
  zero code and zero YAML edits.
- **Concurrency** (charting): per-Agent `max_concurrent`, default 1, queued.
  Plus a per-Runner cap.

### Resolved tickets

- **[Build the Broker; reuse nothing for it](issues/02-age-multi-recipient-and-broker.md)**
  (02): every existing local-daemon secret tool brokers a *key*, we need a
  *value*. `sops keyservice` is a blind decryption oracle and its `exec-env`
  orphans children on `SIGTERM`, which would silently break Teardown. Caller
  identity is a **per-Run socket in a per-Run directory**, bind-mounted (the
  directory, never the file) into only that Run's container. **macOS cannot
  bind-mount a unix socket.** One age file per Agent, not per secret.
  **The Broker verdict was later overtaken by ADR-0002**, which removed the
  Broker entirely — but the `sops exec-env` signal bug survives as a direct
  constraint on `dsecrets`, and the macOS finding is what made the no-daemon
  design attractive.

- **[The `ssh://` Runner is not config-only](issues/03-docker-sdk-over-ssh.md)**
  (03): the Docker Go SDK does **not** speak `ssh://` — `WithHost("ssh://...")`
  returns no error and TCP-dials the literal string; that lives in `docker/cli`'s
  connhelper. Docker Desktop has **no headless story** (autostart is a login
  event, off by default; the boot request is closed and locked), so the Mac must
  auto-login or run Colima. There is **no liveness detection** over the
  transport — a silent stall hangs a log stream forever. And Docker Desktop runs
  the daemon with a 2s shutdown timeout, so a Run in flight during an engine
  restart can lose its Journal; pin ≥ 4.88.0. Two Runners still stands as a
  decision, but the premise that it was free is dead → re-taken in ticket 10.
  Teardown gains two constraints that have nothing to do with SSH, both on ticket
  06: `agent & wait $!`, and an internal self-limit.

- **[Neither CLI has a usable turn limit](issues/01-headless-claude-and-codex.md)**
  (01): `claude --max-turns` is now hidden from `--help`; `codex` has no turn,
  step or tool-call cap at all. Both have a **token budget** instead, so that is
  what the Agent YAML takes. Codex's is `features.rollout_budget` — verified to
  hard-stop a run, but under development, undocumented and off by default. A
  **codex network partition retries forever**, which promotes the time-kill from
  policy to mandatory backstop. **Sandbox denials exit 0** and codex's structured
  error codes are flattened to a string in exec mode, so limit-vs-crash cannot be
  read off `$?` — scan the event stream instead. The `--json` stream *is* rich
  enough for the Journal (8 event types, 9 item types, schema captured). Trap for
  Teardown: **rollout files are zstd-compressed when cold**, so a `*.jsonl` glob
  misses them.

- **[The remote Runner stays, on a forwarded socket](issues/10-recost-the-remote-runner.md)**
  (10): re-taken with the real cost visible and kept in v1. Transport is **not**
  connhelper `ssh://` but an `autossh` sidecar holding one supervised `ssh -L`,
  dropping the daemon socket on a shared volume — so both Runners are plain
  `unix://` paths and the control plane branches on nothing. Deletes seven of the
  fourteen breakage rows and removes the need for a `docker` CLI on the Mac,
  killing the `PATH` trap. The daemon on the Mac is the operator's choice; it
  supplies one value, the socket path.

- **[The Agent YAML schema](issues/04-agent-yaml-schema.md)**
  (04): required is `name`, `agent`, `prompt`; everything else defaults.
  **`personality` is a free-text string** mapped to `--append-system-prompt` /
  `-c developer_instructions=` — but claude appends where codex replaces, so the
  schema says so rather than pretending. **No unified budget field**:
  `limits.wall_clock` is the only universal limit and CLI-specific caps go
  through verbatim `extra_args`, because claude caps dollars and codex caps
  weighted tokens. **Repos are declared, refs are not** — Teardown must know what
  to push, but the agent checks out its own commit from the payload, so
  no-templating survived its strongest counterexample. Triggers are a list; the
  Run learns which fired from `TRIGGER_KIND`/`TRIGGER_NAME`. `catch_up: false` by
  default. **No hot reload**, and a bad file **fails the whole startup**.
  Prototype: [`prototype/04-agent-yaml/`](prototype/04-agent-yaml/).

- **[`dsecrets` is a shell script, not a daemon or a Go binary](issues/05-broker-protocol.md)**
  (05): `dsecrets NAME[,NAME...] -- cmd`, ~20 lines of POSIX shell over `age -d`
  and `jq`, ending in `exec` — so signals reach the child because nothing is left
  in the way, making ticket 02's `sops exec-env` orphaning bug structurally
  impossible. Secrets travel as **one `DSECRETS_ENVELOPE`** (a single age
  ciphertext over a flat JSON object) — **the envelope was later superseded by
  ADR-0003**, which replaced local decryption with a control-plane call. What
  survived: the command-line shape, `exec` rather than fork, `has()` checked
  before the value is read, `jq -j` plus a sentinel keeping values byte-exact,
  **no file sink and no decrypt-everything mode**, and every failure exiting
  non-zero without running the child. Accepted hole: the **model credential is
  plaintext in the agent's own environment**, because the CLI needs it.

- **[The entrypoint and teardown contract](issues/06-entrypoint-contract.md)**
  (06): the container **fetches** everything over a new **Run API** rather than
  having it pushed in — the control plane cannot write files to a remote Runner,
  it has only the Docker API. The agent runs as `& wait $!` (a trap cannot fire
  under a foreground command), stdout goes to a **file** not a pipe (claude drains
  stdout for up to 30s), stdin is `</dev/null` (codex hangs otherwise), and a
  background `sleep && kill -TERM 1` enforces the wall clock from **inside**,
  because codex retries a partition forever. One `trap teardown EXIT` plus
  `trap 'exit 143' TERM` gives one teardown path for every exit. **Work pushes
  first, Journal last**, so the Journal records the work push's outcome —
  reversed from the original recommendation because heartbeats mean the Journal is
  no longer the single point of loss. Outcome is learned from **both** ends: the
  container reports, and the control plane polls `ContainerInspect`, because only
  the latter sees a container that was killed without warning. Skills, clone, or
  payload failure **aborts**. `build_argv` lives in the entrypoint, not in Go, so
  a vendor renaming a flag is an image rebuild rather than a control-plane
  redeploy.

- **[The Run API](issues/11-the-run-api.md)** (11): three endpoints —
  `GET /run/payload`, `POST /run/status`, `POST /run/secrets` — over **plain HTTP
  on the Tailscale tailnet**, because WireGuard already gives encryption and
  device identity and TLS on top would be a cert lifecycle for no added
  protection. Token is **opaque and stored**, not a JWT, so revocation is instant.
  A denied secret is a **403 naming the names**, never a silent omission — the
  failure ticket 02 rejected `summon` for. Abuse is **throttled and surfaced,
  never auto-killed**, because a killing threshold is a guess that fails by
  killing a legitimate long Run near the end. Three missed heartbeats mark a Run
  stale **and trigger an `Inspect`** — a gap is a hint, not a verdict — and
  `Inspect` wins any disagreement. Runs get **internet plus the control plane and
  nothing else** on the tailnet or LAN, enforced by iptables *inside* the
  container so both Runners match, **which forces the agent to run unprivileged**
  (folded back into 06). Governing principle: the API is read-only about the Run
  and write-only about its own status — no Run can start another, read another,
  or touch config.

- **[The Journal is object storage, not git](issues/07-journal-format.md)**
  (07): the `agentruns` repository is **dropped**. The decisive argument is
  Teardown's budget — a git push needs a clone first, and that cost grows with
  every Run ever recorded, while object PUTs are flat forever. This is the code
  most likely to be racing a `SIGKILL`. Everything else follows: lifecycle rules
  make retention configuration, deleting a leaked secret is a `DELETE` rather than
  a history rewrite, concurrent writes need no retry at all, and `ListObjects`
  **is** the index. Two objects per Run — a small separately-fetchable
  `meta.json` so metadata greps across a thousand Runs without unpacking any, and
  `run.tar.zst` for the rest. Access is **presigned PUTs minted at Teardown**, so
  no storage credential enters a container and no Run can overwrite another's
  record. `terminal_reason` is read from the **event stream, not `$?`**. The work
  branch stays git — it is real code.

### Known ceiling

The container holds a write credential for the Journal repository, so a Run can
edit its own record before teardown fires. Chosen deliberately over
control-plane capture, for the simplicity of a single push path. Revisit if the
Journal is ever used for anything an agent has an incentive to distort.

**A Run's bearer token is equivalent to its Allowlist.** Under ADR-0003 no key
material is in the container, but `RUN_TOKEN` will fetch anything the Agent is
permitted, and the agent can read its own environment. The blast radius is
unchanged from the key-in-container design; what improved is that the credential
is centrally revocable and every use is logged. **The model credential remains
plaintext in the agent's own environment**, because the CLI consumes it — no
channel design changes that.

**Secrets need the control plane reachable.** A Run continues working through an
outage, but any command needing a secret fails until the link returns.

**Nothing scrubs secrets out of a Journal.** `dsecrets` keeps plaintext out of
the agent's environment, but an agent that deliberately echoes a value puts it in
the event stream, and nothing in the container knows the values to redact.
Accepted: private bucket, and the first thing to revisit if the Journal is ever
shared.

**Limit-vs-crash is not structurally distinguishable for codex in exec mode**
(ticket 01). The error codes exist internally and are flattened to a bare string;
recovering them means `codex app-server`. v1 reads the event stream and accepts
the ambiguity.

## Not yet specified

- **Live log streaming to the UI.** Deferred to v2 at ticket 06: the Run API is
  the obvious home for it, and streaming over that channel would delete the
  docker-logs-across-the-link problem entirely, but it brings buffering,
  backpressure and ordering with it. Ticket 03 answered the Docker-side half if
  it is ever needed — resume with `Timestamps` plus an inclusive, self-disabling
  `Since`, de-duplicate at the boundary, and run an idle watchdog.
- **Failure handling for a Run.** Retry, backoff, dead-letter, alerting. Unclear
  whether v1 needs anything beyond "it's in the Journal".
- **Cost and token accounting per Run.** Ticket 01 captured the event schema, so
  the raw material exists; what is still open is what the Journal should record
  and whether the two CLIs report it comparably.
- **Agent-authored decision notes.** An `agentrun note` command that records
  intent mid-Run, on top of the mechanical stream. Deferred; additive.
- **Secret rotation.** Re-encrypting the corpus when the master key rotates.
  Runner enrolment is no longer part of this — ADR-0003 left the control plane as
  the only holder of key material.
- **Journal retention.** Deferred at ticket 07, and no longer hard: object
  lifecycle rules express it as configuration. Choosing the policy needs data this
  system has not produced yet.
- **Chained Runs.** One Run triggering another. Suspected to be out of scope but
  not yet ruled.
- **Prompt templating.** Deferred at charting in favour of the payload file;
  revisit only if a real Trigger source demands it.

## Out of scope

- **Kubernetes, Nomad, Swarm, and multi-tenancy.** The destination is two
  Runners on hardware you own.
- **Per-Agent Docker images and a build pipeline or registry.** Ruled out at
  charting in favour of one shared base image.
- **Browser-based YAML editing.** Ruled out at charting; configuration lives in
  git.
- **Automatic pull requests or merging of agent work.** Runs push a branch and
  stop there.
