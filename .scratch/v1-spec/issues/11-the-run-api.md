# The Run API: the control plane ↔ Run channel

Type: grilling
Status: resolved
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

## Answer

### Transport: plain HTTP over Tailscale

The private network is a **Tailscale tailnet**, so WireGuard already provides
encryption and device identity end to end. Plain HTTP over it. **No TLS, no
certificates, no CA distribution, no rotation** — layering TLS on top of
WireGuard would be a lifecycle maintained forever for no added protection.

This is contingent on the tailnet. If the control plane ever moves onto a
physical LAN, TLS is required and the CA goes in as an environment variable that
the entrypoint writes to a file — never baked into the image, so rotation stays a
config change.

### The endpoints

```
GET  /run/payload   → 200, the raw trigger body. `{}` for a schedule Trigger,
                      so the agent always has a file to parse.
POST /run/status    → {status, message, exit_code} → 204
POST /run/secrets   → {names: [...]} → 200 {NAME: value}
                                       403 {denied: [...]}
GET  /run/journal-urls → 200 {meta: <presigned PUT>, archive: <presigned PUT>}
```

The fourth endpoint was added by ticket 07: Teardown fetches two presigned PUT
URLs for object storage, so no storage credential ever enters a container. Minted
on request rather than at spawn, so a long Run cannot outlive them. Read-only
about the Run itself, which is why it fits.

401 for an absent or bad token; 403 for denied names, or for any request from a
Run the control plane already considers terminal; 404 for an unknown Run; 429 for
throttling.

**A denied name is a 403 naming the offending names, never a silent omission.**
`dsecrets` depends on this: ticket 02 rejected `summon` specifically for dropping
denied names quietly, and reproducing that failure here would undo the reason the
tool exists.

### The token

Opaque, 32 random bytes, base64url. **Stored, not a JWT.** The control plane
already holds a Run record, so the lookup is free and revocation is instant; a
stateless token would need a denylist, which is state anyway with cryptography in
front of it. Minted at spawn, injected as `RUN_TOKEN`, invalidated the moment the
Run reaches a terminal state.

A request bearing a terminal Run's token gets 403 **and is logged loudly** — it
means a zombie process or something stranger, and it is exactly the signal worth
not burying.

### Rate limiting

**Throttle and surface it. Never auto-kill.** A per-Run token bucket; over the
limit returns 429, `dsecrets` fails loudly and locally, and the Run appears
flagged as throttled in the UI and in its Journal — not merely in a log line.

Auto-killing was rejected because the threshold is a guess, and the way a guessed
threshold fails is killing a legitimate ninety-minute Run at minute eighty-five
because it genuinely needed credentials in a loop. Throttling costs that Run
time; killing costs it everything.

### Heartbeat

30s interval. **Three missed beats marks the Run stale and triggers an immediate
`ContainerInspect`.** A heartbeat gap is a hint to go and ask Docker, never a
conclusion on its own — the two signals answer different questions. A container
that is SIGKILLed or loses power cannot report its own death, and only `Inspect`
sees that; equally, `Inspect` cannot see a Run that is wedged but alive, and only
the heartbeat gap shows that.

### Ordering

**First terminal state wins**; a duplicate or late `finished` is logged and
ignored, so a report racing the `Inspect` poll cannot corrupt the record.
**`ContainerInspect` is authoritative on disagreement** — if the container claims
exit 0 and Docker says 137, Docker is right. A container can be confused about
its own death; Docker cannot.

### Network egress from a Run

Runs get **internet access and the control plane, and nothing else on the
tailnet or the LAN**:

```
allow → the internet            (git, npm, model APIs — agents need it)
allow → control plane tailnet IP, that port only
drop  → 100.64.0.0/10           (the rest of the tailnet)
drop  → 10/8, 172.16/12, 192.168/16
```

Enforced **inside the container**, not in the host's `DOCKER-USER` chain. The
host route breaks on the Mac — Docker's iptables chains live inside the Linux VM,
reachable on Colima and effectively not on Docker Desktop — and in-container
rules behave identically on both Runners, so no divergence.

**This forces the agent to run unprivileged**, which is a new constraint on
ticket 06 and has been folded back into it. A root agent can simply `iptables -F`
and the rules are decoration.

### What is deliberately not on this API

Event streaming stays out until v2 (decided at ticket 06). Beyond that, the
governing principle: **the Run API is read-only about the Run itself and
write-only about its own status.** A Run cannot start another Run, read another
Run's data, list Agents, or touch any configuration.

That principle is what stops the surface accreting. This is the one channel a
language model with a shell and a valid credential can reach the control plane
through, so every endpoint added to it is attack surface, and the default answer
to "could we also expose…" is no.
