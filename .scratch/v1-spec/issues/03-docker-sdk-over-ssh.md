# Docker Go SDK over `ssh://`: what works and what breaks

Type: research
Status:
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
