# 3. Secrets over the Run API

Date: 2026-08-31

## Status

Accepted. Supersedes [ADR-0002](0002-per-run-key-envelope.md).

## Context

ADR-0002 shipped an age identity into each Run's container because the
alternative — a Broker — cost a daemon per Runner, a socket protocol, and a
permanent macOS divergence, since macOS cannot bind-mount a unix socket into a
container.

Ticket 06 then introduced a **Run API**: an authenticated channel from a Run back
to the control plane, added for trigger-payload delivery and liveness reporting.
That channel changes the price of everything ADR-0002 rejected. Secrets over an
existing HTTP channel cost one endpoint. There is no unix socket, so there is no
macOS problem. And the control plane is already holding the master identity.

The cost argument that decided ADR-0002 no longer holds, so the decision is
re-taken rather than inherited.

## Decision

`dsecrets` asks the control plane. It sends the names it wants with the Run's
bearer token; the control plane checks them against that Agent's Allowlist,
decrypts the permitted ones with the master identity, logs the access, and
returns the plaintext. `dsecrets` then `exec`s the child with those values in the
child's environment.

**No key material of any kind enters a Run's container.** There is no age
identity, no envelope, and no `age` binary in the base image.

The Allowlist becomes a **server-side permission check** rather than a statement
about which ciphertext happens to exist.

## Consequences

This is the goal ADR-0001 set — no keys in the container — reached without its
cost. There is no per-Runner daemon, no socket protocol, and both Runners are
identical, because the transport is HTTP on the private network.

**Per-access audit logging returns.** ADR-0002 gave it up explicitly; the control
plane now sees every request, for which names, from which Run.

**A Run depends on the control plane being reachable in order to use a secret.**
This is in tension with the decision that a Run continues working when the
control plane is unreachable (ticket 06): it does continue, but any command
needing a secret fails until the link returns. Accepted — the failure is loud,
local, and recoverable, and the control plane is on the same private network as
both Runners.

The Run's **bearer token is now equivalent to its Allowlist**, exactly as the age
identity was under ADR-0002. The blast radius is unchanged; what changes is that
the credential is revocable centrally and its use is logged.

The model credential exception from ADR-0002 stands: `ANTHROPIC_API_KEY` /
`CODEX_API_KEY` is in the agent process's own environment because the CLI
consumes it, and no channel design changes that.

Plaintext secrets now travel over the network. On a private network this is
tolerable, but the Run API's transport security is a real decision and belongs to
the Run API ticket, not to this one.

## Amendments

**2026-09-03, from ticket 06.** Built as decided, with two refinements. The
model credential exception stands, with the name corrected for claude: the
CLI runs on the Claude subscription, so the value is `CLAUDE_CODE_OAUTH_TOKEN`
from `claude setup-token`, never `ANTHROPIC_API_KEY`, which the CLI would
prefer and bill API credits with (ticket 03). It is the one Allowlist value
decrypted at spawn rather than on demand, and the Allowlist is its sole
source: the control plane's own environment no longer supplies it, not even
as a fallback. And the master identity is read at every use rather than held;
nothing is decrypted at startup, and a deploy whose key file is missing,
readable by others, or malformed fails before it serves.
