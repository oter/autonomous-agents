# Docker Go SDK over `ssh://`: what works and what breaks

Type: research
Status: resolved
Blocked by:

## Question

Charting assumed the remote Mac mini Runner is "config, not code" because the
Docker Go client speaks `ssh://` natively. That assumption carries the whole
two-Runner design, so verify it.

- How does the official Docker Go SDK connect to `ssh://user@host`? What does it
  shell out to, what does it need on the client, and how does it authenticate —
  agent forwarding, key file, both?
- Does the full API surface work over that transport, specifically: create with
  resource limits, start, attach or log-follow, `stop` with a grace period, and
  remove?
- Log streaming over SSH — does it stay open for a long-running container, and
  what happens when the link drops mid-Run? A dropped connection must not orphan
  a container or lose a Journal.
- Docker Desktop on macOS: does it expose the socket in a way `ssh://` can reach,
  and does anything about the VM change resource limits or signal delivery?
  `SIGTERM`-then-grace is load-bearing for teardown.
- What breaks that does not break on a local socket? Enumerate honestly — this
  is where a "free" design turns expensive.

Desk research against documentation and source. Ticket 08 does the live check.

## Answer

Full findings: [`research/03-docker-sdk-over-ssh.md`](../research/03-docker-sdk-over-ssh.md).
1209 lines, backed by pinned source (moby v28.5.2, docker/cli v29.7.2), man
pages, live experiments through the identical `commandconn` transport, and issue
states re-checked against the GitHub API.

**Verdict: the `ssh://` Runner is not viable as config-only.** It is viable with
roughly a day of code and configuration, plus one hole that is not about SSH at
all. The decision to have two Runners stands; the premise that it was free does
not. §6 enumerates fourteen things that break remotely and do not break locally.

**The four findings that change the design:**

1. **The Docker Go SDK does not speak `ssh://`.** Verified by running it:
   `WithHost("ssh://user@host")` returns **no error** and then TCP-dials the
   literal string `user@host`. The `ssh://` handling lives in `docker/cli`'s
   `connhelper` package, so we take a heavyweight module dependency and wire
   three options in a specific order. Upstream issue open since 2023.
2. **Docker Desktop has no headless story, and this is the biggest structural
   risk.** It is a per-user GUI application; autostart is a *login* event and is
   off by default. With nobody signed in, there is no daemon. The upstream
   request to start at boot is closed and locked. The only unattended pattern is
   auto-login plus "start on sign-in", which fights FileVault. **Colima is the
   honest alternative** — CLI-only, launchd-managed, no GUI app.
3. **There is no liveness detection over `ssh://` whatsoever.** `SetDeadline` is
   a no-op, the SDK's hijack keepalive is skipped because the connection is not a
   `*net.TCPConn`, and connhelper never sets `ServerAliveInterval`. A silently
   wedged transport hangs a log stream forever with no error — reproduced.
4. **The Docker Desktop engine-shutdown hole.** `docker stop -t 30` itself is
   sound: enforced daemon-side under `context.WithoutCancel`, transport-
   independent, verified live. But Docker Desktop deliberately runs the daemon
   with a 2-second shutdown timeout, and before 4.82 stamped `StopTimeout: 1` on
   every container. A Run in flight when the engine restarts — reboot,
   auto-update, sleep — may get ~2s instead of 30 **and lose its Journal**. Pin
   the Runner to Docker Desktop ≥ 4.88.0.

**Two constraints that land on ticket 06, neither caused by SSH:**

- **The entrypoint must run the agent as `agent & wait $!`.** A shell trap does
  not fire until the foreground command returns, so without this the 30 seconds
  of grace buys nothing and teardown never runs.
- **A Run must enforce its own time limit internally.** "Time is a container kill
  via a context deadline" is insufficient remotely: the control plane cannot kill
  what it cannot reach, and an OOM kill is `SIGKILL`, so the trap never runs
  either way.

**One fog item is now answered.** Live log streaming does change with the remote
Runner: resume with `Timestamps: true` plus `Since:` — nanosecond precision,
**inclusive**, and it self-disables after the first match — then de-duplicate at
the boundary and run an idle watchdog. The UI transport question is still open.

**A better transport exists and is worth taking now.** Forwarding the daemon
socket over one persistent SSH connection —
`ssh -N -o ServerAliveInterval=15 -o ExitOnForwardFailure=yes -L /run/macmini-docker.sock:<remote>/docker.sock` —
with `DOCKER_HOST=unix:///run/macmini-docker.sock` deletes rows 1, 3, 4, 7, 9, 10
and 11 from §6 outright: no `docker/cli` dependency, no custom transport, no
hidden `dial-stdio`, no version-negotiation flakiness, no per-connection SSH
handshake, and **no `docker` CLI needed on the Mac at all**, which erases the
`PATH` trap entirely. It costs one supervised process (`autossh`) in the control
plane image.

The research recommends starting with `ssh://` and keeping the Runner behind a
thin interface. **Disagreeing on the laziness argument**: importing `docker/cli`,
wiring connhelper, provisioning a `docker` CLI on the remote PATH, and adding a
watchdog for a transport with no liveness detection is more moving parts than one
`autossh` process against the SDK's first-class `unix` path. Carried into ticket
10 as a decision rather than settled here.
