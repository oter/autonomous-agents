# The container entrypoint and teardown contract

Type: grilling
Status:
Blocked by: 01, 04, 05

## Question

Specify what happens inside a Run's container, in order, from `docker start` to
exit. This is the ticket that makes "the container pushes at teardown" real.

- The full sequence: install skills, fetch secrets, write the trigger payload,
  clone whatever the Run works on, start the agent process, capture its output,
  then teardown.
- **Where does each phase's failure go?** If skills fail to install, does the Run
  proceed without them, or abort? Charting gave skill installation its own
  timeout — say what happens when it expires.
- Teardown must fire on every exit path: clean exit, agent crash, and `SIGTERM`
  from a timeout kill. Specify the trap, what runs inside it, and its own time
  budget inside the 30-second `docker stop` grace period. What happens if
  teardown itself exceeds it.
- What does teardown push, in what order, and what happens if the Journal push
  succeeds but the work push fails, or the reverse?
- Which parts of this are baked into the base image and which are generated per
  Run by the control plane?
- What is the contract with the control plane — how does the control plane learn
  a Run's outcome, given the container may be on the remote Runner?

> **Constraints from ticket 03:** the entrypoint must run the agent as
> `agent & wait $!` — a shell trap does not fire while a foreground command is
> running, so without this the 30s grace buys nothing and teardown never runs.
> And a Run must enforce its own time limit internally: the control plane cannot
> kill what it cannot reach, and an OOM kill is `SIGKILL` regardless.

> **From ticket 01:** codex rollout files are **zstd-compressed when cold**, so a
> `*.jsonl` glob at Teardown silently misses them. Sandbox denials exit `0` and
> `error_seen` is sticky, so the outcome must be read from the event stream, not
> `$?`. A codex network partition retries forever with no timeout — the internal
> self-limit is mandatory, not defensive.
