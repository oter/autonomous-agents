# 01: Agent YAML + control-plane config loader

**What to build:** Starting the control plane binary against a config file and an `agents_dir` loads every Agent definition, applies the documented defaults, and refuses to start at all if any Agent file is malformed — the operator sees which file and why. A valid tree starts cleanly and can enumerate its Agents.

**Blocked by:** None (can start immediately).

**Status:** resolved

- [x] Control-plane config (listeners, ui auth, agents_dir, image, stop_grace, runners, master identity path, journal endpoint/bucket) parses per SPEC §3
- [x] Agent schema per SPEC §2: `name`/`agent`/`prompt` required, everything else defaults (`runner: local`, `wall_clock: 30m`, conservative memory default — never unbounded, `max_concurrent: 1`, empty skills/repos/secrets/triggers)
- [x] Trigger auth shapes validate: `hmac_sha256` requires header/encoding/secret; `bearer` requires secret; `none` requires nothing
- [x] Duplicate Agent names, unknown `agent` values, and unknown `runner` names fail startup
- [x] One malformed Agent file fails the entire startup with a message naming the file — no partial load, no hot reload
- [x] `personality` is carried with its per-CLI meaning intact (claude appends, codex replaces) rather than flattened
- [x] Verified against the sample Agent YAMLs left by the spec prototype

## Answer

Implemented as `internal/config`, one public seam: `config.Load(path) (*Config, error)`.
It parses the control-plane YAML per SPEC §3, globs `agents_dir` (resolved
relative to the config file when not absolute), applies the SPEC §2 defaults
(`runner: local`, `wall_clock: 30m`, `memory: 2g`, `max_concurrent: 1`, empty
collections), and validates everything. Any failure aborts the whole load with
an error that names the offending file. Decoding is strict (`KnownFields`), so
unknown keys fail startup too. `personality` is kept verbatim next to the
`agent` kind; the per-CLI flag mapping happens at spawn (ticket 03), not here.

Memory default is `2g`. Trigger validation also requires `path`+`auth` on
webhooks (with `scheme: none` as the explicit opt-out) and `cron` on schedules.

Two-axis review (Standards + Spec) found no standards breaches and three spec
gaps, all fixed: a missing or mistyped `agents_dir` now fails startup
(`os.ReadDir` instead of glob), `.yml` files load and any stray file or
subdirectory in `agents_dir` fails startup loudly (hidden dotfiles such as
`.DS_Store` are ignored), and `max_concurrent` must be at least 1 so an
explicit `0` cannot silently create an Agent that never runs.

Tests: `internal/config/load_test.go` — TDD at the `Load` seam only, including
a test that loads the three prototype sample Agent YAMLs from
`.scratch/v1-spec/prototype/04-agent-yaml/agents` unmodified. `go test ./...`,
`go vet`, `gofmt` all clean. Module initialized as
`github.com/oter/autonomous-agents` (go 1.27, dep: `gopkg.in/yaml.v3` only).
