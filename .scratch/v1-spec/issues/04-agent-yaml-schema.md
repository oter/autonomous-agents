# The Agent YAML schema, and the control-plane config

Type: prototype
Status:
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
