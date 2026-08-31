# 1. Runner-local secret broker

Date: 2026-08-31

## Status

Superseded by [ADR-0002](0002-per-run-key-envelope.md)

## Context

Runs are LLM agents with an unrestricted shell. They need credentials (git
access, third-party API keys) but must not be able to read credentials their
Agent was not granted, and must not be able to emit plaintext credentials into
their own output — a Run's Journal is pushed to a repository, and a model that
has a secret in its context can leak it into a commit message, a log line, or a
chat reply without ever intending to.

Three arrangements were considered:

- **Central broker.** Only the control plane holds key material; `dsecrets`
  calls it over the network. Smallest blast radius, but a Run on the remote
  Runner fails whenever the control plane is unreachable.
- **Runner-local broker.** Each Runner holds its own age identity. Secrets are
  encrypted to multiple recipients, so each secret names which Runners may
  decrypt it.
- **Key in the container.** Ciphertext and key both handed to the Run;
  `dsecrets` decrypts locally. Simplest, and offline by construction.

The third was rejected on inspection: "encoded so that only the tool can
decode" does not survive contact with a shell. Anything `dsecrets` needs in
order to decrypt without a remote authority is present on the machine, and can
be recovered from the binary, the filesystem, or the environment. It obscures
rather than restricts, and it makes the tool's existence hard to justify.

## Decision

Each Runner holds an age identity and runs a small broker daemon. Key material
never enters a Run's container. `dsecrets` connects to the broker on its own
host over a unix socket, names the secrets it wants, and receives only those
that the calling Agent's allowlist permits; it then executes the child command
with those values in the child's environment.

Secrets are stored as age ciphertext inside the Agent YAML, encrypted to the
recipients that are allowed to read them — the control plane, plus whichever
Runners need them.

## Consequences

A Run continues to work while the control plane is unreachable, because its
Runner can decrypt on its own. Compromising one Runner yields only the secrets
encrypted to that Runner, not the whole keyring.

Plaintext exists only in the environment of the child process `dsecrets`
spawns, so it stays out of the agent's own environment and out of its context.
Every decryption passes through the broker and can be logged.

The cost is a second daemon to deploy and an age identity to provision per
Runner, and secrets must be re-encrypted when a Runner is added.
A Run can still capture a secret deliberately, and more easily than "only in the
child's environment" suggests: any process running as the same uid can read
`/proc/<child>/environ` while the child lives, so no wrapping trick is even
required. This boundary limits accidental disclosure — it keeps plaintext out of
the model's context, which is where a leak becomes a commit message — and it
constrains scope to the Allowlist. It does not contain a hostile agent.

## Amendments

**2026-08-31, from ticket 02.** Two refinements, neither of which changes the
decision.

The Broker must be **name-addressed**: it takes secret names and returns values,
never ciphertext-in/key-out. This is what makes the Allowlist and the audit log
mean anything. The distinction is not academic — `sops keyservice` satisfies
every sentence of the Decision above while being a blind decryption oracle that
logs nothing useful, because its request carries no secret name and no caller.

**A macOS host cannot bind-mount a unix socket into a container.** The Mac mini
Runner therefore runs its Broker containerised, sharing a Docker volume with the
Run, rather than exposing a host socket path. The two Runners diverge here; the
`dsecrets` side of the contract does not change.
