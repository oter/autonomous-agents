# 08: Schedule Triggers

**What to build:** An Agent with a schedule Trigger starts a Run on its cron expression in its declared timezone. The payload endpoint serves `{}` for scheduled Runs. Missed ticks (control plane down, redeploy in progress) are skipped, not replayed.

**Blocked by:** 03 (Walking-skeleton Run).

**Status:** ready-for-agent

- [ ] Cron expressions evaluated in the Trigger's timezone, DST handled by the timezone database rather than hand-rolled offsets
- [ ] `catch_up: false` is the default and the only v1 behavior: a tick that passed while the scheduler wasn't running is skipped
- [ ] A schedule-triggered Run's payload is `{}`
- [ ] Multiple Triggers on one Agent coexist (webhook + schedule on the same Agent both work)
- [ ] Demo: an every-minute Agent fires on schedule; stopping the control plane across a tick and restarting produces no replayed Run
