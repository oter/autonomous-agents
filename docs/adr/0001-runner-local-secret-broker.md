# 1. Runner-local secret broker

Date: 2026-08-31

## Status

Accepted

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

A Run can still capture a secret deliberately — by wrapping a command that
prints it, for example. This boundary limits accidental disclosure and
constrains scope; it does not contain a hostile agent.
