# 4. The container writes its own Journal

Date: 2026-08-31

## Status

Accepted

## Context

Every Run has to leave a durable record — what it was asked to do, what it did,
and what it concluded — so that behaviour can be analysed across Runs and
execution tuned. Two arrangements were available.

**The control plane captures.** It is already the parent of the container and
both CLIs emit a structured event stream, so it could tee that stream to storage
without any cooperation from the agent. The record would then be impossible for
the observed process to alter, and impossible for a lazy model to skip.

**The container writes.** An entrypoint wrapper traps exit — including the
`SIGTERM` of a timeout kill — and uploads the record itself.

## Decision

The container writes, at Teardown.

The wrapper traps every exit path and uploads the Journal before the process
ends. The agent is never relied upon to remember; the trap is what guarantees the
record, not the model's cooperation.

## Consequences

The failure mode this avoids is the one that matters most in practice: an agent
that finishes its work and forgets the last step. A trap has no memory to fail.

Teardown becomes load-bearing, which is why several other decisions exist to
protect it. The agent must run as `agent & wait $!`, because a shell trap cannot
fire while a foreground command is running. `stop_grace` is 90 seconds rather
than 30, because `claude` drains its stdout for up to 30 seconds on its own.
Journals go to object storage rather than git ([ADR-0005](0005-journals-in-object-storage.md)),
because a git push needs a clone whose cost grows without bound while this code
is racing a `SIGKILL`.

**A Run can edit its own record.** It holds the credentials for the upload and
the files are on its own filesystem until the moment they leave. Control-plane
capture would have made the record tamper-evident by construction; this does not.
Accepted deliberately, twice, for the simplicity of a single write path. Revisit
if the Journal is ever used for something an agent has an incentive to distort.

**A container cannot witness its own hard death.** A `SIGKILL`, an OOM kill or a
power loss leaves no record, because Teardown never runs. This is why the control
plane also polls `ContainerInspect` and treats its exit code as authoritative:
not as a second Journal, but as the only observer of an unannounced death.
