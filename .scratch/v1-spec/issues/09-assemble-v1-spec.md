# Assemble the v1 spec and the remaining ADRs

Type: grilling
Status:
Blocked by: 04, 05, 06, 07, 08

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
