# 12: Read-only UI

**What to build:** A human on the tailnet opens the UI, authenticates with basic auth, and can see every Agent, every Run with its outcome, and any throttle-flagged Runs — and can start a Run with a "run now" button. Nothing on the UI edits configuration; config lives in git.

**Blocked by:** 03 (Walking-skeleton Run), 04 (Run API hardening).

**Status:** ready-for-agent

- [ ] Served only on the tailnet-bound UI listener with bcrypt basic auth from config
- [ ] Lists Agents (name, CLI, runner, triggers) and Runs (id, status, exit, duration, trigger)
- [ ] Throttled/flagged Runs are visibly marked
- [ ] "Run now" starts a Run — the UI's only write, and it writes to the same spawn path as every other Trigger
- [ ] No YAML editing, no config mutation, no secret display anywhere in the UI
