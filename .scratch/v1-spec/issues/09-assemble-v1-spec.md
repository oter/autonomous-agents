# Assemble the v1 spec and the remaining ADRs

Type: grilling
Status: resolved
Blocked by: 04, 05, 06, 07, 11

## Question

The terminal ticket: turn everything the map resolved into the document a fresh
session implements from.

- Write the spec. The Agent YAML schema, the control plane's Run lifecycle, the
  entrypoint and teardown contract, the Broker interface, and the Journal format
  — as one coherent artifact rather than nine answers.
- Write ADRs for the hard-to-reverse calls this map made that are not yet
  recorded. ADR-0001 covers the secret broker. Candidates: the container pushing
  its own Journal over control-plane capture, the split public/private route
  surface, and one shared base image over per-Agent images. Apply the usual bar —
  hard to reverse, surprising without context, a real trade-off — and skip
  anything that misses it.
- Sweep the map's **Not yet specified** section. Anything still there at this
  point is either genuinely a later effort, or a gap in the spec. Say which.
- State plainly what a v1 implementation is not required to do.

> **Unblocked from ticket 08.** 08 verifies the **`macmini` Runner only**. The
> `local` Runner is a plain unix socket on Linux — no forwarded socket, no
> Docker Desktop, no VM — so nothing 08 can discover changes the control plane,
> the Agent YAML, the Run API, `dsecrets`, the entrypoint, or the Journal. Ticket
> 10's forwarded-socket transport is exactly what bought this: both Runners are
> `docker_host: unix://…` and the control plane branches on nothing. The spec is
> written for both; 08 confirms one of them and its findings can only produce
> **macmini-local rework**, never a redesign.

## Answer

**The spec is [`SPEC.md`](../../../SPEC.md)** at the repo root — twelve sections
covering the Agent YAML, the control-plane config, the base image, the Run
lifecycle, the entrypoint and Teardown contract, network egress, `dsecrets`, the
Run API, and the Journal, as one artifact rather than eleven answers.

### ADRs written

Two more, both meeting all three criteria — hard to reverse, surprising without
context, and the result of a real trade-off:

- **[ADR-0004](../../../docs/adr/0004-the-container-writes-its-own-journal.md)** —
  the container writes its own Journal. Surprising because control-plane capture
  is the obvious design and would have been tamper-evident by construction; the
  trade is a single write path and a trap that cannot forget.
- **[ADR-0005](../../../docs/adr/0005-journals-in-object-storage.md)** — Journals
  in object storage rather than git. Surprising because the project's own
  charting specified a git repository; the trade is losing `git log` to gain a
  Teardown cost that does not grow with every Run ever recorded.

**Deliberately not written**: the split public/private route surface, and one
shared base image over per-Agent images. Both are decisions, but neither is
surprising once stated — Linear cannot POST to a private network, and a build
pipeline for one image is obviously not worth it. They live in the spec prose.
The forwarded-socket transport was the closest call; it is genuinely surprising,
but swapping it is a sidecar and a config value, so it fails the hard-to-reverse
test and sits in §3 of the spec with its full reasoning.

### Fog swept

**Cost and token accounting has graduated** — ticket 07 settled it. `meta.json`
records `total_cost_usd` for claude and tokens only for codex, and the schema
records that divergence rather than inventing a common number.

**Chained Runs moved to out of scope.** Ticket 11 forbids a Run starting another
Run, and did so as a security boundary rather than as an unimplemented feature.
It stops being fog.

Everything else in **Not yet specified** is a genuine later effort, not a gap:
live log streaming, agent-authored notes, Run failure handling, Journal
retention, secret rotation, and prompt templating. All are listed in §11 of the
spec so an implementer knows they were decided against rather than missed.

### The honest state

The map's destination is reached: nothing is left to decide before someone builds
this. **Ticket 08 remains open and is not a blocker** — it verifies the `macmini`
Runner, and the `local` Runner is a plain unix socket on Linux. Its findings can
produce macmini-local rework, never a redesign. §12 of the spec lists it first
among the open risks.
