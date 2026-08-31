# The `dsecrets` interface and the per-Run key envelope

Type: grilling
Status:
Blocked by: 02

## Question

[ADR-0002](../../../docs/adr/0002-per-run-key-envelope.md) replaced the Broker
with a per-Run key envelope: the control plane re-encrypts an Agent's allowlisted
secrets to a fresh per-Run age identity, injects the ciphertext as environment
variables and the identity as a file, and `dsecrets` decrypts locally and `exec`s
the child. This ticket pins down that contract. It no longer specifies a daemon
or a wire protocol.

- **The command line.** Charting sketched `dsecrets --flags {what to decrypt
  into what} -- actual-cmd`. Settle the real shape: how a caller names secrets,
  how a secret name maps onto an environment variable name for the child, and
  whether decrypting to a **file** (an SSH key, a credentials JSON) is supported
  or deliberately not.
- **`exec`, not fork, and forward signals.** Ticket 02 measured `sops exec-env`
  orphaning its child on `SIGTERM`. The same bug here silently breaks Teardown
  and loses the Journal of every timed-out Run — so specify the process model
  explicitly and say how it is tested.
- **How the ciphertext is carried.** One environment variable per secret, or one
  envelope variable holding all of them? Ticket 02 found one age file per Agent
  is 2.7x smaller than one per secret; the same question applies here, and the
  answer interacts with how `env` output looks to a curious agent.
- **What happens on a name that has no ciphertext.** Under ADR-0002 the
  Allowlist is enforced by what ciphertext exists, so an unknown name is not a
  permission denial — it is a missing variable. Fail loudly regardless: ticket 02
  found `summon` silently drops denied names, which is the wrong default.
- **Where the identity file lives, and its lifecycle.** Path, mode, whether the
  entrypoint removes it after the agent process starts, and what that costs if a
  Run needs to decrypt something later in its life.
- **Failure modes**: identity file missing, ciphertext undecryptable by this
  Run's identity, malformed envelope. Each should produce an error a human can
  act on from the Journal alone.
- **Does `dsecrets` need to exist, given ADR-0002?** Ticket 02 ruled out `sops
  exec-env` on the signal bug and `summon` on fail-open. Confirm that verdict
  still holds now that there is no Broker and no runtime Allowlist check — the
  tool is much smaller than it was, and the bar for writing it should be re-met,
  not inherited.
