# 01: Agent YAML + control-plane config loader

**What to build:** Starting the control plane binary against a config file and an `agents_dir` loads every Agent definition, applies the documented defaults, and refuses to start at all if any Agent file is malformed — the operator sees which file and why. A valid tree starts cleanly and can enumerate its Agents.

**Blocked by:** None (can start immediately).

**Status:** ready-for-agent

- [ ] Control-plane config (listeners, ui auth, agents_dir, image, stop_grace, runners, master identity path, journal endpoint/bucket) parses per SPEC §3
- [ ] Agent schema per SPEC §2: `name`/`agent`/`prompt` required, everything else defaults (`runner: local`, `wall_clock: 30m`, conservative memory default — never unbounded, `max_concurrent: 1`, empty skills/repos/secrets/triggers)
- [ ] Trigger auth shapes validate: `hmac_sha256` requires header/encoding/secret; `bearer` requires secret; `none` requires nothing
- [ ] Duplicate Agent names, unknown `agent` values, and unknown `runner` names fail startup
- [ ] One malformed Agent file fails the entire startup with a message naming the file — no partial load, no hot reload
- [ ] `personality` is carried with its per-CLI meaning intact (claude appends, codex replaces) rather than flattened
- [ ] Verified against the sample Agent YAMLs left by the spec prototype
