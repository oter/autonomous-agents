# The Run API: the control plane ↔ Run channel

Type: grilling
Status:
Blocked by:

## Question

Ticket 06 introduced an authenticated channel from a Run back to the control
plane, and [ADR-0003](../../../docs/adr/0003-secrets-over-the-run-api.md) then
moved secrets onto it. Three components now depend on it, so it needs specifying
on its own rather than in fragments.

Both Runners are on the same private network as the control plane, so this is a
plain private-network route — no tunnelling, no public exposure.

- **The endpoints.** Ticket 06 uses `GET /run/payload`, `POST /run/status`, and
  `POST /run/secrets`. Confirm that set, their request and response shapes, and
  their status codes. `dsecrets` depends on a denied name being a **403 with the
  offending names**, never a silent omission.
- **Transport security.** Plaintext secrets travel in a response body. On a
  private network that is arguable, but it is a decision, not an oversight: TLS
  with a self-signed cert and a pinned CA, plain HTTP, or something else. Note
  the control plane is a container in Coolify and one Runner is a Mac, so
  whatever is chosen has to be provisionable on both.
- **The Run token.** Minted at spawn, injected as `RUN_TOKEN`, scoped to one Run.
  Settle its format, whether it is stored or verified statelessly, when it is
  revoked, and what happens to a request bearing the token of a Run the control
  plane believes has already finished.
- **Rate limiting and abuse.** The caller is an LLM with a shell and a valid
  token. A Run that loops on `/run/secrets` should be throttled and visible, not
  merely logged. Decide what the control plane does about it.
- **The heartbeat contract.** 30s in ticket 06 — confirm it. How many missed
  beats before a Run is marked stale, what marking it stale actually does, and
  how that reconciles with the `ContainerInspect` poll that runs alongside.
- **Idempotency and ordering.** A `finished` report can arrive after the
  `Inspect` poll already recorded an exit, or twice. Neither should corrupt the
  Run record.
- **What is deliberately not on it in v1.** Event streaming was deferred to v2
  (ticket 06). Say what else is out, so the surface does not accrete: this is the
  one channel an agent can reach the control plane through, and every endpoint on
  it is attack surface reachable by a language model.
