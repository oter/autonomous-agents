# 09: Concurrency + queueing

**What to build:** Triggers that fire faster than their Agent or Runner can absorb produce queued Runs, not dropped ones. `max_concurrent` holds per Agent and per Runner, and a queued Run starts as soon as a slot frees.

**Blocked by:** 03 (Walking-skeleton Run).

**Status:** ready-for-agent

- [ ] Per-Agent `max_concurrent` enforced (default 1)
- [ ] Per-Runner `max_concurrent` enforced independently
- [ ] Over either limit, the Run is queued — never dropped, never an error to the Trigger
- [ ] A queued Run starts when a slot frees, and its Journal reflects real start time vs trigger time
- [ ] Demo: fire an Agent with `max_concurrent: 1` three times fast; exactly one container at a time, all three complete
