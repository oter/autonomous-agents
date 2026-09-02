# 04: Run API hardening — heartbeats, staleness, throttling

**What to build:** The Run API becomes the trustworthy channel of SPEC §9. A healthy Run heartbeats and stays green; a silent Run gets checked against Docker rather than guessed about; an abusive Run gets throttled and flagged but never killed by the API.

**Blocked by:** 03 (Walking-skeleton Run).

**Status:** resolved

- [x] Entrypoint heartbeats status every 30s; three missed heartbeats mark the Run stale and trigger an immediate ContainerInspect — a gap is a hint to ask Docker, never a conclusion
- [x] First terminal state wins; ContainerInspect is authoritative on disagreement
- [x] Full status semantics: 401 absent/bad token, 403 on a terminal Run, 404 unknown Run
- [x] Per-Run token bucket returns 429 when exceeded; throttle events are recorded on the Run (surfaced later in UI and Journal); nothing auto-kills
- [x] A Run that loses the control plane keeps running and can finish its work without help; only secret-needing commands fail until the link returns
- [x] Governing principle enforced in routing: the Run API is read-only about the Run itself and write-only about its own status — no route lists Agents, touches config, or reaches another Run

## Answer

Ticket 03 left the store, the API skeleton, and the Inspect poller; this
ticket hardens them in place. No new package, no new dependency, no config
key. The entrypoint already heartbeated every 30s and swallowed every
report failure with `|| true`; its one change is where the heartbeat loop
starts (see the review notes).

- **Status semantics in the API middleware** (`internal/run/api.go`), in
  order: 401 bad token, 404 unknown Run, 403 terminal Run, 429 throttled,
  then the two routes. With an opaque stored token the token *is* the Run
  identity, so "bad" and "unknown" had to be split by shape: a bearer that
  cannot be a `RUN_TOKEN` (absent, not `Bearer`, not 32 base64url bytes) is
  401; a well-formed one no Run holds is 404 and logged — after a
  control-plane restart that log line is the orphan-container signal. A
  request from a terminal Run is 403 and logged with the exit it was
  recorded with (a zombie, or something stranger). SPEC §9 gained the
  sentence that says so.
- **Per-Run token bucket in the store** (`Store.Allow`): 30 requests, then
  one per second, every route counted. A refusal is 429 with `Retry-After:
  1`, counted on the Run as `Throttled`/`LastThrottled` for tickets 05 and
  12 to surface; the first refusal per Run is logged, the rest are not (a
  loop would drown the log). Nothing on the 429 path touches Terminal.
  Fifteen lines of stdlib rather than `x/time/rate`, which is not a
  dependency yet.
- **Staleness** (`Store.MarkStale`, set from the poller): `Seen` is updated
  by every authenticated request, allowed or throttled, so a throttled Run
  is noisy, not wedged. The poller marks a live Run stale on the tick that
  finds no request for `StaleAfter` (3 × 30s) and that same tick's Inspect
  is the immediate one; the warn line carries Docker's answer. Docker
  saying "running" leaves the Run alive and flagged; the next request clears
  the flag; only Docker's exit ends it. Detection granularity is the poll
  interval (5s).
- **Routing** is unchanged and now pinned by a test that probes ten paths
  under four methods with a valid token and expects 404 everywhere, plus
  405 on the wrong method for the two real routes.

### Tests

Unit (`go test ./...`): bucket arithmetic and refill; stale transition,
clearing, and never-on-terminal; the four status codes end to end through
the handler; the poller flagging and un-flagging a Run the fake Docker keeps
reporting as running; the surface probe. The routing probe mints a fresh Run
per path because the throttle correctly bit the first version of it.

### Demo (2026-09-02, OrbStack, `agent-base:dev` rebuilt with the moved heartbeat, no subscription token in the shell)

`AA_IMAGE=agent-base:dev go test ./internal/run -run WalkingSkeleton -v`
now has a third case and all three pass in about 20s, before and after the
review fixes:

- trivial claude Run: exit 1 (no credential in this shell, as in ticket 03's
  first demo), `finished` reported, Inspect confirmed. The hardened
  middleware sat in the path of every request the entrypoint made — payload
  fetch, finished report — and none was refused.
- codex Run with `wall_clock: 5s`: exit 143 from the trap, `finished`
  reported, Inspect confirmed, Journal present. The Teardown report landed
  *before* the Run was terminal, so the 403 rule never bit it; that ordering
  is structural (the report precedes the exit that Inspect sees).
- codex Run with `wall_clock: 10s` **whose Run API listener was closed the
  moment its payload fetch landed**: the CLI kept retrying its own 401s,
  the internal wall clock fired, Teardown ran and wrote `meta.json` with
  exit 143, the finished report failed silently, and the control plane
  recorded exit 143 from Inspect with no report at all. That is SPEC §9's
  "if the control plane is unreachable, the Run continues", demonstrated.

### Deliberate shortcuts, all marked `ponytail:` in code

- One fixed bucket size for every Run (30 burst, 1/s). A per-Agent limit in
  the YAML is the upgrade if one Agent legitimately needs more.
- Throttle events are a count and a last-seen time, not a list. Ticket 05
  can widen that if the Journal needs episodes.
- Run records are still in memory and now carry the bucket; ticket 03's
  persistence note stands.

### Follow-ups for later tickets

- Ticket 11 inserts `install_skills` and `clone_repos` at the marked line,
  which is now *after* the heartbeat loop starts; the wall clock still
  starts after them.
- Ticket 06's `dsecrets` must treat 429 as "retry after a second", not as a
  denial; the `Retry-After` header is there for it.
- Tickets 05 and 12 read `Stale`, `Throttled`, and `LastThrottled` off the
  Run record for the Journal and the UI.
- The published image that carries the moved heartbeat is
  `ghcr.io/oter/agent-base:2026-09-04` (the next free date-shaped tag;
  `2026-09-03` was minted earlier the same day for the base-image bump).
  `2026-09-03` and earlier behave identically until ticket 11 adds the
  install, so nothing needs to move to it yet.

### Review (two-axis, Standards and Spec, before commit)

Acted on:

- **§6/§9 conflict** (Spec): SPEC §6 started the heartbeat loop after
  `install_skills` (own 300s timeout) and `clone_repos`, so a Run quietly
  installing for two minutes would have been marked stale. The loop now
  starts right after the payload fetch, in both the SPEC script and
  `image/entrypoint.sh`, with a load-bearing bullet in §6. The wall clock
  still starts after the install, because the install does not consume it.
- **SPEC pinned a guess** (Spec): the bucket size was written into §9 and
  marked `ponytail:` in code at the same time. The numbers are out of the
  SPEC; the code and its marker are the single source.
- **TOCTOU on a finishing Run** (Standards): the middleware checked
  `Terminal` on a snapshot; a Run finishing between that check and the
  handler could have had its last report overwritten. `Store.Report` now
  ignores a terminal Run under the lock.
- Test hygiene (Standards): the second Run API server in the integration
  test is cleaned up; its inline poll loop and the `wait` helper are one
  predicate-taking helper; the `map[bool]string` method pick is a table.
- Vocabulary (Standards): `CONTEXT.md` gained **Heartbeat** (with stale)
  and **Throttle event**.

Dismissed, with the reason:

- *"Immediate Inspect" is really "on the next 5s tick"* (Spec). Yes: the
  tick that notices the gap is the one that asks Docker, and the poller
  asks Docker every tick anyway, so a separate trigger would add a timer
  and save at most five seconds. Stated in the code comment and above.
- *`StaleAfter` has no slack for the heartbeat's request time* (Spec). A
  slow but successful beat makes a 40s gap (30s sleep plus the 10s
  `--max-time`), never 90s. Only two consecutive *failed* beats plus their
  timeouts reach 90s, and a Run whose reports fail twice in a row is
  exactly one to ask Docker about. The flag clears on the next success.
- *A forged well-formed token logs a warning per request* (Spec). Only a
  container on the tailnet can reach the listener, and it holds a real
  token; the warning is the orphan-after-restart signal and is worth the
  theoretical spam. Revisit if it ever happens.
- *Two concurrent first refusals both log "run throttled"* (Standards).
  Two identical warn lines in a race window; the count on the Run is the
  record.
- *`Allow` on an unknown id would be 429 not 404* (Standards). Unreachable:
  nothing removes a Run, and the middleware only calls it with an id it
  just resolved.
- *`Spawner.StaleAfter` beside the const `StaleAfter`* (Standards): follows
  the `PollInterval` precedent from ticket 03.
- *`Retry-After: 1` and `sleep 30` duplicate Go constants* (Standards):
  three literals in three places, each next to a comment naming the other.
  An env var for the heartbeat interval is the upgrade if either ever
  changes.
