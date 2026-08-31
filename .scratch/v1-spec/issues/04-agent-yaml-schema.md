# The Agent YAML schema, and the control-plane config

Type: prototype
Status: resolved
Blocked by: 01, 02

## Question

Write a concrete, complete example Agent YAML — two or three of them, for
genuinely different Agents — and the control-plane config alongside. The point
is a rough artifact to react to, not a specification document; the schema falls
out of arguing about a real file.

Cover the fields charting already committed to: limits (time, turns), the secret
allowlist and its inline age ciphertext, personality, skills to install,
triggers (webhook path plus auth scheme, and schedule), `runner`,
`max_concurrent`, and the static prompt.

Open questions to settle while drafting:

- **What is "personality" concretely** — a prompt string, a mounted instructions
  file, or just another skill? This is currently sitting in the map's fog; if it
  resolves here, clear it from **Not yet specified**.
- Which fields are required, and what does every optional field default to?
- Can one Agent have several triggers, and does a Run need to know which one
  fired it?
- Schedule syntax: cron, and what happens to ticks missed while the control
  plane was down. Catch-up, skip, or run once.
- What splits between the Agent YAML and the control-plane config? Runner
  definitions, age recipients, UI credentials, and the Journal repository all
  belong to the deployment rather than to any Agent.
- Where do the files live, how are they discovered, and does a change get picked
  up without a restart?
- What does validation reject at load time, and does one bad file take down the
  server or just its own Agent?

> **From ticket 01:** the limit field is a **token budget**, not turns — neither
> CLI has a usable turn abstraction. Codex needs `features.rollout_budget`
> (undocumented, off by default, both sub-keys mandatory); `claude --max-turns`
> is hidden from `--help` and may be going away. Decide how one YAML field maps
> onto two different mechanisms, and what happens if `rollout_budget` disappears.

## Answer

Prototype asset: [`prototype/04-agent-yaml/`](../prototype/04-agent-yaml/) —
three deliberately unalike Agents plus `control-plane.yaml`, with every verdict
recorded inline as a `# DECIDED:` comment.

### Required, and everything else

Required: `name`, `agent` (`claude` | `codex`), `prompt`. Everything else
defaults: `runner: local`, `personality: ""`, `skills: []`, `secrets: {}`,
`repos: []`, `limits.wall_clock: 30m`, `extra_args: []`, `triggers: []`,
`max_concurrent: 1`. An Agent with no triggers is legal and can only be started
from the UI.

### Personality — the fog item, cleared

A single free-text string, mapped to `--append-system-prompt` for claude and
`-c developer_instructions=` for codex. No mounted file, no new mechanism.

The two are **not** equivalent and the schema should not pretend otherwise:
claude appends to the system prompt, codex replaces the developer instructions,
and no append variant exists for codex. One line in the spec, not a blocker.

### Limits — no unified budget field

`limits.wall_clock` is the only universal limit, because **neither CLI has a
wall clock of its own**. CLI-specific caps go through `extra_args`, passed
verbatim.

A shared `budget:` key was rejected as a lie: claude caps **dollars**
(`--max-budget-usd`, which overshot a $0.0001 cap by 350x in testing — a stop
condition, not a guarantee), codex caps **weighted tokens** through an
undocumented, off-by-default, under-development config key. Verbatim passthrough
also means the schema does not change when the CLIs change their flags.

### Triggers

A list, so one Agent can have several with different auth. The Run learns which
one fired from `TRIGGER_KIND` and `TRIGGER_NAME` in its environment, alongside
`/run/trigger.json`.

Schedules are cron plus an explicit `timezone`. **`catch_up: false` is the
default**: ticks missed while the control plane was down are skipped, not
replayed, so a redeploy cannot stampede. Set it true only for genuinely
idempotent Agents.

### Repos — declared, but not their refs

**The Agent declares which repositories it works on.** Charting wanted the agent
to clone whatever it needed, keeping the control plane out of the git business,
but Teardown has to push a work branch and therefore something must know which
checkout *is* the work. Declaring it is the smaller lie than an entrypoint
guessing.

**Refs stay the agent's job**, and this survived its strongest counterexample: a
PR-review Agent needs the pull request's head SHA, which lives only in the
trigger payload. Templating that one field was rejected — the agent reads
`/run/trigger.json` and checks out the commit itself, which is one sentence in
the prompt. Adding one blessed interpolation is how a config file becomes a
language.

### The split

The Agent YAML holds what belongs to an Agent: name, CLI, personality, prompt,
skills, secrets, repos, limits, triggers, runner, concurrency. The control-plane
config holds what belongs to the deployment: listen addresses, UI credentials,
Runner definitions and their caps, the base image, the master age identity path,
the Journal repository, and `stop_grace`.

### Discovery, reload, validation

Agents are a glob over `agents_dir`. **No hot reload in v1** — a change needs a
redeploy, which in Coolify *is* the reload, and skipping it avoids fsnotify and
the "what happens to in-flight Runs" question entirely.

**A bad Agent file fails the whole startup** rather than quarantining itself.
Configuration comes from reviewed git, and a loudly failed deploy beats one Agent
silently not running. The cost is accepted: one typo stops everything.

### Two things that fell out and belong to ticket 06

`stop_grace` is **90s, not 30s**. `claude` drains its stdout before exiting and
will wait up to 30 seconds on its own if the consumer reads slowly, which would
consume the entire grace and leave nothing for the Journal push.

Better still, and the real fix: **the entrypoint writes the event stream to a
file, not to a pipe the control plane reads.** That removes the drain risk at
source, and simultaneously removes the need to hold a log stream open across SSH
— which ticket 03 found has no liveness detection whatsoever. Two problems, one
fix.

### Amended: resource limits

Agents run on the **same Docker host as the control plane** on the `local`
Runner, which the original answer did not account for. `limits` needs
`memory` and `cpus` alongside `wall_clock` — Docker enforces both at container
create, so this is a field, not a feature.

Without them, `max_concurrent: 6` on a modest VM lets Runs starve the control
plane that supervises them, and the failure looks like the whole platform
degrading rather than one Agent misbehaving. Defaults should be conservative and
explicitly set rather than left unbounded.
