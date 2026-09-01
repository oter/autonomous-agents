# 03: Walking-skeleton Run — "run now" to recorded exit

**What to build:** The tracer bullet. Hitting a run-now endpoint for an Agent starts a real Run: a container spawns on the `local` Runner, the CLI executes the Agent's prompt, and the control plane records the outcome. From this ticket on, the system demonstrably runs agents end to end; everything later widens this path.

**Blocked by:** 01 (Agent YAML + config loader), 02 (Base image).

**Status:** ready-for-agent

- [ ] Run id allocated in the documented format (timestamp, agent name, hex suffix) and an opaque `RUN_TOKEN` minted per Run — stored, not a JWT
- [ ] Container created over the local unix socket with the Agent's memory/cpu limits and per-Run environment; stop grace from config
- [ ] Minimal Run API for the entrypoint: payload endpoint returning `{}` for now, status endpoint accepting reports; 401 on absent/bad token
- [ ] Entrypoint follows the SPEC §6 shape exactly — every awkward line there is load-bearing and measured: agent started with `& wait` (never foreground, or the EXIT trap can't fire), `trap 'exit 143' TERM` routing stop-kills through the one teardown path, stdout to a file (claude stalls on slow pipes), `</dev/null` (codex hangs on open stdin), and the internal wall-clock `sleep && kill -TERM 1` (a codex network partition retries forever; the control plane cannot kill what it cannot reach)
- [ ] Teardown runs on every exit path — success, failure, TERM at the wall clock — and leaves a local Journal directory (stream, stderr, CLI rollout files including the compressed `.zst` variants, a minimal meta)
- [ ] Control plane polls ContainerInspect; outcome recorded preferring Inspect's exit code; first terminal state wins
- [ ] Demo: run-now on a trivial Agent completes; the same Agent with a tiny `wall_clock` is TERM-killed and still leaves its Journal
