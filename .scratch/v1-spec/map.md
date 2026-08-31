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
- **Runner-local secret broker** (charting): see
  [ADR-0001](../../docs/adr/0001-runner-local-secret-broker.md). Key material
  lives on each Runner, never in a Run's container. Per-Agent allowlist in the
  YAML.
- **age for encryption** (charting): `filippo.io/age`, multi-recipient, no cloud
  KMS. Ciphertext inline in the Agent YAML.
- **One shared base image** (charting): both CLIs and the `skills` CLI baked in.
  Per-Agent skills installed at container start with `skills add ... -y`, which
  supports GitHub shorthand, arbitrary git URLs, and local paths, and targets
  both `claude-code` and `codex`. Skill installation gets its own timeout and
  does not consume the Run's budget.
- **Runs are ephemeral** (charting): no persistent volume in v1. Durability
  comes from the Journal.
- **The container pushes, at teardown** (charting): an entrypoint wrapper traps
  exit — including `SIGTERM` from a timeout kill — and commits and pushes both
  the Journal and the Run's work. `docker stop --time=30` gives it room. The
  agent is never relied on to remember.
- **Journal shape** (charting): one private repository for all Agents, laid out
  `<agent>/<run-id>/`, run id `20260831-201204-<agent>-<4 hex>` so that
  filenames sort chronologically and concurrent Runs cannot collide.
- **Work pushes to a branch** (charting): `agents/<agent>/<run-id>`, always,
  never to `main`, no automatic pull request. Failed Runs push too.
- **One git credential** (charting): a fine-grained PAT with a repository
  allowlist, shared by the Journal push and the work push. Not deploy keys —
  those are inherently one per repository, which contradicts "one key".
- **Limits use what already exists** (charting): time is a container kill via a
  context deadline; turns pass through to the CLI's own flag. No custom
  accounting.
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

### Known ceiling

The container holds a write credential for the Journal repository, so a Run can
edit its own record before teardown fires. Chosen deliberately over
control-plane capture, for the simplicity of a single push path. Revisit if the
Journal is ever used for anything an agent has an incentive to distort.

## Not yet specified

- **What "personality" concretely is.** A system prompt string, a mounted
  `CLAUDE.md`/`AGENTS.md`, or a skill. Likely resolves inside the schema ticket,
  but may graduate on its own.
- **Live log streaming to the UI.** Server-sent events, websockets, or polling —
  and whether the remote Runner changes the answer.
- **Failure handling for a Run.** Retry, backoff, dead-letter, alerting. Unclear
  whether v1 needs anything beyond "it's in the Journal".
- **Cost and token accounting per Run.** Whether the CLIs report enough to make
  this free.
- **Agent-authored decision notes.** An `agentrun note` command that records
  intent mid-Run, on top of the mechanical stream. Deferred; additive.
- **Secret rotation and Runner enrolment.** Re-encrypting the corpus when a
  Runner is added or a key is rotated.
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
