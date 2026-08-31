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

> **Constraint from ticket 02:** macOS cannot bind-mount a unix socket into a
> container. The Mac mini Runner runs its Broker containerised on a shared Docker
> volume; the local Runner uses a host directory. Bind-mount the *directory*, never
> the socket file — a file mount pins the inode and a Broker restart leaves running
> containers on `ECONNREFUSED`.
