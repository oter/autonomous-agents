# 2. A per-Run key envelope instead of a Broker

Date: 2026-08-31

## Status

Accepted. Supersedes [ADR-0001](0001-runner-local-secret-broker.md).

## Context

ADR-0001 put an age identity on each Runner behind a daemon, so that no key
material ever entered a Run's container. Ticket 02 confirmed that design was
buildable in about 150 lines and that nothing off the shelf could replace it.

It was rejected on a simpler observation about the shape of a Run: the container
starts, does its work, finishes, and dies. Key material inside it is as
short-lived as the container. Against that, the Broker costs a daemon per Runner,
a socket protocol, a per-Run socket directory, and — because macOS cannot
bind-mount a unix socket into a container — a permanent divergence between the
two Runners.

The standing objection to "encrypted so that only our tool can decrypt" is that
a key which the tool can use without a remote authority is present on the machine
and recoverable, and the thing sharing that machine is an LLM with a shell. That
objection was raised, considered, and the trade accepted. The decision below is
shaped to remove the objection's force rather than to argue with it.

## Decision

At spawn, the control plane decrypts the Agent's allowlisted secrets with the
master identity, generates a **fresh age identity for that Run alone**,
re-encrypts only those secrets to it, and injects the ciphertext as environment
variables together with the identity as a file at mode 0600. `dsecrets` reads the
ciphertext from its own environment, decrypts it with the identity file, and
`exec`s the child process with the plaintext in the **child's** environment. The
identity is generated per Run and discarded with it.

The Allowlist is therefore enforced by **what ciphertext exists in the
container**, not by a runtime permission check.

## Consequences

No Broker daemon, no socket protocol, no per-Run socket directory. **The macOS
bind-mount divergence disappears entirely** — the two Runners become identical
with respect to secrets, which was the single largest source of Runner-specific
complexity in the design.

Extracting the Run's identity yields exactly that Agent's Allowlist, for the
lifetime of one Run — which is precisely what the Agent was granted in the first
place. The key is not worth protecting, and that is what makes shipping it into
the container acceptable. A per-Runner or per-Agent identity would not have this
property; the per-Run envelope is what earns the trade.

Plaintext still stays out of the agent's own environment by default. An
accidental `env` dump into a log line or a commit message shows ciphertext. This
is the remaining, real purpose of `dsecrets`, and it survives the change intact.

**Access auditing is lost.** The Broker could log every decryption; a local
decrypt cannot. If per-access auditing is ever wanted, it comes back with the
Broker.

**A Run can read its own plaintext** — deliberately, and the ceiling is the
Allowlist. Any same-uid sibling process can also read `/proc/<child>/environ`
while the child lives, so the child-environment boundary is a hygiene measure,
not a containment one. This was already true of the Broker design.

`dsecrets` **must `exec` rather than fork, and must forward signals.** Ticket 02
measured `sops exec-env` orphaning its child on `SIGTERM`; the same bug in
`dsecrets` would silently break Teardown and lose the Journal of every timed-out
Run.
