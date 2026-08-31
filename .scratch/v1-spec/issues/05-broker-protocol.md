# The Broker wire protocol and the `dsecrets` interface

Type: grilling
Status:
Blocked by: 02

## Question

Pin down the contract between a Run and its Runner's broker.

- The command line. Charting sketched `dsecrets --flags {what to decrypt into
  what} -- actual-cmd`. Settle the real shape: how a caller names secrets, how it
  maps a secret name onto an environment variable name, and whether decrypting to
  a file (an SSH key, a credentials JSON) is supported or deliberately not.
- **How does the broker know which Agent is calling?** Ticket 02 surveys the
  options; decide here. Peer credentials over the unix socket, a per-Run token
  injected at spawn, or the socket path itself being per-Run. This is what makes
  the allowlist mean anything.
- What does the broker do on a denied name — fail the whole invocation, or pass
  through what was permitted? Fail loudly is the default position.
- What gets logged per access, and where does that log go: the control plane, the
  Journal, or both? An audit trail the Run can edit is not an audit trail.
- Does `dsecrets` `exec` the child or fork it, and what happens to signals? A
  timeout `SIGTERM` has to reach the agent process through the wrapper, or
  teardown never fires.
- Failure modes: broker down, socket missing, ciphertext undecryptable by this
  Runner. Each should produce an error a human can act on.

> **Constraint from ticket 02:** macOS cannot bind-mount a unix socket into a
> container. The Mac mini Runner runs its Broker containerised on a shared Docker
> volume; the local Runner uses a host directory. Bind-mount the *directory*, never
> the socket file — a file mount pins the inode and a Broker restart leaves running
> containers on `ECONNREFUSED`.
