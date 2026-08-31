# Re-cost the remote Runner: keep it, and on what daemon and transport

Type: grilling
Status:
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
