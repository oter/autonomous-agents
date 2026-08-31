# Re-cost the remote Runner: keep it, and on what daemon and transport

Type: grilling
Status: resolved
Blocked by:

## Question

Charting kept the Mac mini Runner on the premise that it was "config, not code".
[Ticket 03](03-docker-sdk-over-ssh.md) demolished that premise: the Docker Go SDK
does not speak `ssh://` at all, Docker Desktop has no headless story, and there
is no liveness detection over the transport. The decision may well stand, but it
has to be re-taken with the real price on the table.

Three decisions, in order — the first can make the other two moot.

**1. Does the remote Runner stay in v1?** It costs roughly a day of code, a
second credential class, a permanent operational surface, and a macOS box that
must stay logged in. Against that: it is hardware already owned, and Runs that
want Apple silicon or a residential IP have nowhere else to go. Dropping it does
not delete the Runner abstraction — `local` still needs it — it only defers the
remote implementation. If it goes, it goes to **Out of scope**, not to fog.

**2. If it stays, which daemon runs on the Mac?** Docker Desktop with macOS
auto-login and "start on sign-in" — which fights FileVault, and must be pinned
to ≥ 4.88.0 for the engine-shutdown fix — or **Colima**, which is CLI-only and
launchd-managed and exists precisely for this. Note that the non-interactive
`PATH` trap in §5.2 of the research applies to all of them; it is an sshd
problem, not a Docker Desktop problem.

**3. If it stays, which transport?** The `connhelper` `ssh://` path the research
recommends starting with, or a **forwarded unix socket** (`ssh -L` plus
`autossh`), which deletes seven of the fourteen breakage rows in §6 and removes
the need for a `docker` CLI on the Mac entirely. The counter-argument for
`ssh://` is fewer moving parts; the counter-counter-argument is that a
`docker/cli` dependency plus a hand-rolled watchdog is not fewer moving parts
than one supervised process.

Whatever is chosen, decide how thin the Runner interface has to be for the other
option to remain a config change rather than a rewrite.

## Answer

**1. The remote Runner stays in v1.** Re-taken with the real cost visible and
kept anyway.

**2. The daemon on the Mac is the operator's choice**, not a design decision.
Whichever is installed, it only supplies one value: the socket path.

**3. Transport is a forwarded unix socket, not connhelper `ssh://`.** An
`autossh` sidecar in the Coolify stack holds one supervised connection and drops
the socket on a volume shared with the control plane:

```
autossh -M 0 -N \
  -o ServerAliveInterval=15 -o ServerAliveCountMax=3 \
  -o ExitOnForwardFailure=yes -o StrictHostKeyChecking=accept-new \
  -L /shared/macmini-docker.sock:<remote-socket-path> runner@macmini
```

Both Runners then have the same shape, and the control plane branches on nothing:

```yaml
runners:
  local:   { docker_host: "unix:///var/run/docker.sock" }
  macmini: { docker_host: "unix:///shared/macmini-docker.sock" }
```

This deletes rows 1, 3, 4, 7, 9, 10 and 11 from §6 of the ticket 03 research: no
`docker/cli` dependency, no custom transport, no hidden `dial-stdio`, no
API-version negotiation flakiness, no per-connection SSH handshake, and **no
`docker` CLI required on the Mac**, which erases the `PATH` trap — the most
likely default failure. Liveness becomes one explicit `ServerAliveInterval`
rather than N invisible connections with no deadlines. SSH keys and `known_hosts`
live in the sidecar, never in the Go binary.

Costs: one more container in the stack, and `AllowStreamLocalForwarding` must be
`yes` in the Mac's `sshd_config` — it is the default.

The ticket 03 research recommended starting with `ssh://` for having fewer moving
parts. Rejected: a heavyweight module dependency plus a hand-rolled liveness
watchdog is not fewer moving parts than one supervised process.

**Still live from ticket 03, and not fixed by this transport**: the Docker
Desktop engine-shutdown hole. If the operator installs Docker Desktop, pin it to
≥ 4.88.0. Ticket 08 verifies.
